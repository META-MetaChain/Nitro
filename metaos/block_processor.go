// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

package METAos

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/big"

	"github.com/META-MetaChain/nitro/METAos/METAosState"
	"github.com/META-MetaChain/nitro/METAos/METAostypes"
	"github.com/META-MetaChain/nitro/METAos/l2pricing"
	"github.com/META-MetaChain/nitro/METAos/util"
	"github.com/META-MetaChain/nitro/solgen/go/precompilesgen"
	"github.com/META-MetaChain/nitro/util/METAmath"

	"github.com/ethereum/go-ethereum/metachain_types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/trie"
)

// set by the precompile module, to avoid a package dependence cycle
var METARetryableTxAddress common.Address
var METASysAddress common.Address
var InternalTxStartBlockMethodID [4]byte
var InternalTxBatchPostingReportMethodID [4]byte
var RedeemScheduledEventID common.Hash
var L2ToL1TransactionEventID common.Hash
var L2ToL1TxEventID common.Hash
var EmitReedeemScheduledEvent func(*vm.EVM, uint64, uint64, [32]byte, [32]byte, common.Address, *big.Int, *big.Int) error
var EmitTicketCreatedEvent func(*vm.EVM, [32]byte) error
var gasUsedSinceStartupCounter = metrics.NewRegisteredCounter("META/gas_used", nil)

type L1Info struct {
	poster        common.Address
	l1BlockNumber uint64
	l1Timestamp   uint64
}

func (info *L1Info) Equals(o *L1Info) bool {
	return info.poster == o.poster && info.l1BlockNumber == o.l1BlockNumber && info.l1Timestamp == o.l1Timestamp
}

func (info *L1Info) L1BlockNumber() uint64 {
	return info.l1BlockNumber
}

func createNewHeader(prevHeader *types.Header, l1info *L1Info, state *METAosState.METAosState, chainConfig *params.ChainConfig) *types.Header {
	l2Pricing := state.L2PricingState()
	baseFee, err := l2Pricing.BaseFeeWei()
	state.Restrict(err)

	var lastBlockHash common.Hash
	blockNumber := big.NewInt(0)
	timestamp := uint64(0)
	coinbase := common.Address{}
	if l1info != nil {
		timestamp = l1info.l1Timestamp
		coinbase = l1info.poster
	}
	extra := common.Hash{}.Bytes()
	mixDigest := common.Hash{}
	if prevHeader != nil {
		lastBlockHash = prevHeader.Hash()
		blockNumber.Add(prevHeader.Number, big.NewInt(1))
		if timestamp < prevHeader.Time {
			timestamp = prevHeader.Time
		}
		copy(extra, prevHeader.Extra)
		mixDigest = prevHeader.MixDigest
	}
	header := &types.Header{
		ParentHash:  lastBlockHash,
		UncleHash:   types.EmptyUncleHash, // Post-merge Ethereum will require this to be types.EmptyUncleHash
		Coinbase:    coinbase,
		Root:        [32]byte{},    // Filled in later
		TxHash:      [32]byte{},    // Filled in later
		ReceiptHash: [32]byte{},    // Filled in later
		Bloom:       [256]byte{},   // Filled in later
		Difficulty:  big.NewInt(1), // Eventually, Ethereum plans to require this to be zero
		Number:      blockNumber,
		GasLimit:    l2pricing.GethBlockGasLimit,
		GasUsed:     0,
		Time:        timestamp,
		Extra:       extra,     // used by NewEVMBlockContext
		MixDigest:   mixDigest, // used by NewEVMBlockContext
		Nonce:       [8]byte{}, // Filled in later; post-merge Ethereum will require this to be zero
		BaseFee:     baseFee,
	}
	return header
}

type ConditionalOptionsForTx []*metachain_types.ConditionalOptions

type SequencingHooks struct {
	TxErrors                []error
	DiscardInvalidTxsEarly  bool
	PreTxFilter             func(*params.ChainConfig, *types.Header, *state.StateDB, *METAosState.METAosState, *types.Transaction, *metachain_types.ConditionalOptions, common.Address, *L1Info) error
	PostTxFilter            func(*types.Header, *METAosState.METAosState, *types.Transaction, common.Address, uint64, *core.ExecutionResult) error
	ConditionalOptionsForTx []*metachain_types.ConditionalOptions
}

func NoopSequencingHooks() *SequencingHooks {
	return &SequencingHooks{
		[]error{},
		false,
		func(*params.ChainConfig, *types.Header, *state.StateDB, *METAosState.METAosState, *types.Transaction, *metachain_types.ConditionalOptions, common.Address, *L1Info) error {
			return nil
		},
		func(*types.Header, *METAosState.METAosState, *types.Transaction, common.Address, uint64, *core.ExecutionResult) error {
			return nil
		},
		nil,
	}
}

func ProduceBlock(
	message *METAostypes.L1IncomingMessage,
	delayedMessagesRead uint64,
	lastBlockHeader *types.Header,
	statedb *state.StateDB,
	chainContext core.ChainContext,
	chainConfig *params.ChainConfig,
	batchFetcher METAostypes.FallibleBatchFetcher,
) (*types.Block, types.Receipts, error) {
	var batchFetchErr error
	txes, err := ParseL2Transactions(message, chainConfig.ChainID, func(batchNum uint64, batchHash common.Hash) []byte {
		data, err := batchFetcher(batchNum)
		if err != nil {
			batchFetchErr = err
			return nil
		}
		dataHash := crypto.Keccak256Hash(data)
		if dataHash != batchHash {
			batchFetchErr = fmt.Errorf("expecting batch %v hash %v but got data with hash %v", batchNum, batchHash, dataHash)
			return nil
		}
		return data
	})
	if batchFetchErr != nil {
		return nil, nil, batchFetchErr
	}
	if err != nil {
		log.Warn("error parsing incoming message", "err", err)
		txes = types.Transactions{}
	}

	hooks := NoopSequencingHooks()
	return ProduceBlockAdvanced(
		message.Header, txes, delayedMessagesRead, lastBlockHeader, statedb, chainContext, chainConfig, hooks,
	)
}

// A bit more flexible than ProduceBlock for use in the sequencer.
func ProduceBlockAdvanced(
	l1Header *METAostypes.L1IncomingMessageHeader,
	txes types.Transactions,
	delayedMessagesRead uint64,
	lastBlockHeader *types.Header,
	statedb *state.StateDB,
	chainContext core.ChainContext,
	chainConfig *params.ChainConfig,
	sequencingHooks *SequencingHooks,
) (*types.Block, types.Receipts, error) {

	state, err := METAosState.OpenSystemMETAosState(statedb, nil, true)
	if err != nil {
		return nil, nil, err
	}

	if statedb.GetUnexpectedBalanceDelta().BitLen() != 0 {
		return nil, nil, errors.New("ProduceBlock called with dirty StateDB (non-zero unexpected balance delta)")
	}

	poster := l1Header.Poster

	l1Info := &L1Info{
		poster:        poster,
		l1BlockNumber: l1Header.BlockNumber,
		l1Timestamp:   l1Header.Timestamp,
	}

	header := createNewHeader(lastBlockHeader, l1Info, state, chainConfig)
	signer := types.MakeSigner(chainConfig, header.Number, header.Time)
	// Note: blockGasLeft will diverge from the actual gas left during execution in the event of invalid txs,
	// but it's only used as block-local representation limiting the amount of work done in a block.
	blockGasLeft, _ := state.L2PricingState().PerBlockGasLimit()
	l1BlockNum := l1Info.l1BlockNumber

	// Prepend a tx before all others to touch up the state (update the L1 block num, pricing pools, etc)
	startTx := InternalTxStartBlock(chainConfig.ChainID, l1Header.L1BaseFee, l1BlockNum, header, lastBlockHeader)
	txes = append(types.Transactions{types.NewTx(startTx)}, txes...)

	complete := types.Transactions{}
	receipts := types.Receipts{}
	basefee := header.BaseFee
	time := header.Time
	expectedBalanceDelta := new(big.Int)
	redeems := types.Transactions{}
	userTxsProcessed := 0

	// We'll check that the block can fit each message, so this pool is set to not run out
	gethGas := core.GasPool(l2pricing.GethBlockGasLimit)

	for len(txes) > 0 || len(redeems) > 0 {
		// repeatedly process the next tx, doing redeems created along the way in FIFO order

		var tx *types.Transaction
		var options *metachain_types.ConditionalOptions
		hooks := NoopSequencingHooks()
		isUserTx := false
		if len(redeems) > 0 {
			tx = redeems[0]
			redeems = redeems[1:]

			retry, ok := (tx.GetInner()).(*types.metachainRetryTx)
			if !ok {
				return nil, nil, errors.New("retryable tx is somehow not a retryable")
			}
			retryable, _ := state.RetryableState().OpenRetryable(retry.TicketId, time)
			if retryable == nil {
				// retryable was already deleted
				continue
			}
		} else {
			tx = txes[0]
			txes = txes[1:]
			if tx.Type() != types.metachainInternalTxType {
				hooks = sequencingHooks // the sequencer has the ability to drop this tx
				isUserTx = true
				if len(hooks.ConditionalOptionsForTx) > 0 {
					options = hooks.ConditionalOptionsForTx[0]
					hooks.ConditionalOptionsForTx = hooks.ConditionalOptionsForTx[1:]
				}
			}
		}

		startRefund := statedb.GetRefund()
		if startRefund != 0 {
			return nil, nil, fmt.Errorf("at beginning of tx statedb has non-zero refund %v", startRefund)
		}

		var sender common.Address
		var dataGas uint64 = 0
		preTxHeaderGasUsed := header.GasUsed
		receipt, result, err := (func() (*types.Receipt, *core.ExecutionResult, error) {
			// If we've done too much work in this block, discard the tx as early as possible
			if blockGasLeft < params.TxGas && isUserTx {
				return nil, nil, core.ErrGasLimitReached
			}

			sender, err = signer.Sender(tx)
			if err != nil {
				return nil, nil, err
			}

			if err = hooks.PreTxFilter(chainConfig, header, statedb, state, tx, options, sender, l1Info); err != nil {
				return nil, nil, err
			}

			// Additional pre-transaction validity check
			if err = extraPreTxFilter(chainConfig, header, statedb, state, tx, options, sender, l1Info); err != nil {
				return nil, nil, err
			}

			if basefee.Sign() > 0 {
				dataGas = math.MaxUint64
				brotliCompressionLevel, err := state.BrotliCompressionLevel()
				if err != nil {
					return nil, nil, fmt.Errorf("failed to get brotli compression level: %w", err)
				}
				posterCost, _ := state.L1PricingState().GetPosterInfo(tx, poster, brotliCompressionLevel)
				posterCostInL2Gas := METAmath.BigDiv(posterCost, basefee)

				if posterCostInL2Gas.IsUint64() {
					dataGas = posterCostInL2Gas.Uint64()
				} else {
					log.Error("Could not get poster cost in L2 terms", "posterCost", posterCost, "basefee", basefee)
				}
			}

			if dataGas > tx.Gas() {
				// this txn is going to be rejected later
				dataGas = tx.Gas()
			}

			computeGas := tx.Gas() - dataGas
			if computeGas < params.TxGas {
				if hooks.DiscardInvalidTxsEarly {
					return nil, nil, core.ErrIntrinsicGas
				}
				// ensure at least TxGas is left in the pool before trying a state transition
				computeGas = params.TxGas
			}

			if computeGas > blockGasLeft && isUserTx && userTxsProcessed > 0 {
				return nil, nil, core.ErrGasLimitReached
			}

			snap := statedb.Snapshot()
			statedb.SetTxContext(tx.Hash(), len(receipts)) // the number of successful state transitions

			gasPool := gethGas
			receipt, result, err := core.ApplyTransactionWithResultFilter(
				chainConfig,
				chainContext,
				&header.Coinbase,
				&gasPool,
				statedb,
				header,
				tx,
				&header.GasUsed,
				vm.Config{},
				func(result *core.ExecutionResult) error {
					return hooks.PostTxFilter(header, state, tx, sender, dataGas, result)
				},
			)
			if err != nil {
				// Ignore this transaction if it's invalid under the state transition function
				statedb.RevertToSnapshot(snap)
				return nil, nil, err
			}

			// Additional post-transaction validity check
			if err = extraPostTxFilter(chainConfig, header, statedb, state, tx, options, sender, l1Info, result); err != nil {
				statedb.RevertToSnapshot(snap)
				return nil, nil, err
			}

			return receipt, result, nil
		})()

		if tx.Type() == types.metachainInternalTxType {
			// METAOS might have upgraded to a new version, so we need to refresh our state
			state, err = METAosState.OpenSystemMETAosState(statedb, nil, true)
			if err != nil {
				return nil, nil, err
			}
			// Update the METAOS version in the header (if it changed)
			extraInfo := types.DeserializeHeaderExtraInformation(header)
			extraInfo.METAOSFormatVersion = state.METAOSVersion()
			extraInfo.UpdateHeaderWithInfo(header)
		}

		// append the err, even if it is nil
		hooks.TxErrors = append(hooks.TxErrors, err)

		if err != nil {
			log.Debug("error applying transaction", "tx", tx, "err", err)
			if !hooks.DiscardInvalidTxsEarly {
				// we'll still deduct a TxGas's worth from the block-local rate limiter even if the tx was invalid
				blockGasLeft = METAmath.SaturatingUSub(blockGasLeft, params.TxGas)
				if isUserTx {
					userTxsProcessed++
				}
			}
			continue
		}

		if tx.Type() == types.metachainInternalTxType && result.Err != nil {
			return nil, nil, fmt.Errorf("failed to apply internal transaction: %w", result.Err)
		}

		if preTxHeaderGasUsed > header.GasUsed {
			return nil, nil, fmt.Errorf("ApplyTransaction() used -%v gas", preTxHeaderGasUsed-header.GasUsed)
		}
		txGasUsed := header.GasUsed - preTxHeaderGasUsed

		METAosVer := types.DeserializeHeaderExtraInformation(header).METAOSFormatVersion
		if METAosVer >= METAostypes.METAosVersion_FixRedeemGas {
			// subtract gas burned for future use
			for _, scheduledTx := range result.ScheduledTxes {
				switch inner := scheduledTx.GetInner().(type) {
				case *types.metachainRetryTx:
					txGasUsed = METAmath.SaturatingUSub(txGasUsed, inner.Gas)
				default:
					log.Warn("Unexpected type of scheduled tx", "type", scheduledTx.Type())
				}
			}
		}

		// Update expectedTotalBalanceDelta (also done in logs loop)
		switch txInner := tx.GetInner().(type) {
		case *types.metachainDepositTx:
			// L1->L2 deposits add eth to the system
			expectedBalanceDelta.Add(expectedBalanceDelta, txInner.Value)
		case *types.metachainSubmitRetryableTx:
			// Retryable submission can include a deposit which adds eth to the system
			expectedBalanceDelta.Add(expectedBalanceDelta, txInner.DepositValue)
		}

		computeUsed := txGasUsed - dataGas
		if txGasUsed < dataGas {
			log.Error("ApplyTransaction() used less gas than it should have", "delta", dataGas-txGasUsed)
			computeUsed = params.TxGas
		} else if computeUsed < params.TxGas {
			computeUsed = params.TxGas
		}

		if txGasUsed > tx.Gas() {
			return nil, nil, fmt.Errorf("ApplyTransaction() used %v more gas than it should have", txGasUsed-tx.Gas())
		}

		// append any scheduled redeems
		redeems = append(redeems, result.ScheduledTxes...)

		for _, txLog := range receipt.Logs {
			if txLog.Address == METASysAddress {
				// L2ToL1TransactionEventID is deprecated in upgrade 4, but it should to safe to make this code handle
				// both events ignoring the version.
				// TODO: Remove L2ToL1Transaction handling on next chain reset
				// L2->L1 withdrawals remove eth from the system
				switch txLog.Topics[0] {
				case L2ToL1TransactionEventID:
					event := &precompilesgen.METASysL2ToL1Transaction{}
					err := util.ParseL2ToL1TransactionLog(event, txLog)
					if err != nil {
						log.Error("Failed to parse L2ToL1Transaction log", "err", err)
					} else {
						expectedBalanceDelta.Sub(expectedBalanceDelta, event.Callvalue)
					}
				case L2ToL1TxEventID:
					event := &precompilesgen.METASysL2ToL1Tx{}
					err := util.ParseL2ToL1TxLog(event, txLog)
					if err != nil {
						log.Error("Failed to parse L2ToL1Tx log", "err", err)
					} else {
						expectedBalanceDelta.Sub(expectedBalanceDelta, event.Callvalue)
					}
				}
			}
		}

		blockGasLeft = METAmath.SaturatingUSub(blockGasLeft, computeUsed)

		// Add gas used since startup to prometheus metric.
		gasUsed := METAmath.SaturatingUSub(receipt.GasUsed, receipt.GasUsedForL1)
		gasUsedSinceStartupCounter.Inc(METAmath.SaturatingCast(gasUsed))

		complete = append(complete, tx)
		receipts = append(receipts, receipt)

		if isUserTx {
			userTxsProcessed++
		}
	}

	binary.BigEndian.PutUint64(header.Nonce[:], delayedMessagesRead)

	FinalizeBlock(header, complete, statedb, chainConfig)

	// Touch up the block hashes in receipts
	tmpBlock := types.NewBlock(header, complete, nil, receipts, trie.NewStackTrie(nil))
	blockHash := tmpBlock.Hash()

	for _, receipt := range receipts {
		receipt.BlockHash = blockHash
		for _, txLog := range receipt.Logs {
			txLog.BlockHash = blockHash
		}
	}

	block := types.NewBlock(header, complete, nil, receipts, trie.NewStackTrie(nil))

	if len(block.Transactions()) != len(receipts) {
		return nil, nil, fmt.Errorf("block has %d txes but %d receipts", len(block.Transactions()), len(receipts))
	}

	balanceDelta := statedb.GetUnexpectedBalanceDelta()
	if !METAmath.BigEquals(balanceDelta, expectedBalanceDelta) {
		// Fail if funds have been minted or debug mode is enabled (i.e. this is a test)
		if balanceDelta.Cmp(expectedBalanceDelta) > 0 || chainConfig.DebugMode() {
			return nil, nil, fmt.Errorf("unexpected total balance delta %v (expected %v)", balanceDelta, expectedBalanceDelta)
		}
		// This is a real chain and funds were burnt, not minted, so only log an error and don't panic
		log.Error("Unexpected total balance delta", "delta", balanceDelta, "expected", expectedBalanceDelta)
	}

	return block, receipts, nil
}

// Also sets header.Root
func FinalizeBlock(header *types.Header, txs types.Transactions, statedb *state.StateDB, chainConfig *params.ChainConfig) {
	if header != nil {
		if header.Number.Uint64() < chainConfig.metachainChainParams.GenesisBlockNum {
			panic("cannot finalize blocks before genesis")
		}

		var sendRoot common.Hash
		var sendCount uint64
		var nextL1BlockNumber uint64
		var METAosVersion uint64

		if header.Number.Uint64() == chainConfig.metachainChainParams.GenesisBlockNum {
			METAosVersion = chainConfig.metachainChainParams.InitialMETAOSVersion
		} else {
			state, err := METAosState.OpenSystemMETAosState(statedb, nil, true)
			if err != nil {
				newErr := fmt.Errorf("%w while opening METAos state. Block: %d root: %v", err, header.Number, header.Root)
				panic(newErr)
			}
			// Add outbox info to the header for client-side proving
			acc := state.SendMerkleAccumulator()
			sendRoot, _ = acc.Root()
			sendCount, _ = acc.Size()
			nextL1BlockNumber, _ = state.Blockhashes().L1BlockNumber()
			METAosVersion = state.METAOSVersion()
		}
		metachainHeader := types.HeaderInfo{
			SendRoot:           sendRoot,
			SendCount:          sendCount,
			L1BlockNumber:      nextL1BlockNumber,
			METAOSFormatVersion: METAosVersion,
		}
		metachainHeader.UpdateHeaderWithInfo(header)
		header.Root = statedb.IntermediateRoot(true)
	}
}