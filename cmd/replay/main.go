// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"

	"github.com/META-MetaChain/nitro/METAos"
	"github.com/META-MetaChain/nitro/METAos/METAosState"
	"github.com/META-MetaChain/nitro/METAos/METAostypes"
	"github.com/META-MetaChain/nitro/METAos/burn"
	"github.com/META-MetaChain/nitro/METAstate"
	"github.com/META-MetaChain/nitro/METAutil"
	"github.com/META-MetaChain/nitro/cmd/chaininfo"
	"github.com/META-MetaChain/nitro/das/dastree"
	"github.com/META-MetaChain/nitro/gethhook"
	"github.com/META-MetaChain/nitro/wavmio"
)

func getBlockHeaderByHash(hash common.Hash) *types.Header {
	enc, err := wavmio.ResolveTypedPreimage(METAutil.Keccak256PreimageType, hash)
	if err != nil {
		panic(fmt.Errorf("Error resolving preimage: %w", err))
	}
	header := &types.Header{}
	err = rlp.DecodeBytes(enc, &header)
	if err != nil {
		panic(fmt.Errorf("Error parsing resolved block header: %w", err))
	}
	return header
}

type WavmChainContext struct{}

func (c WavmChainContext) Engine() consensus.Engine {
	return METAos.Engine{}
}

func (c WavmChainContext) GetHeader(hash common.Hash, num uint64) *types.Header {
	header := getBlockHeaderByHash(hash)
	if !header.Number.IsUint64() || header.Number.Uint64() != num {
		panic(fmt.Sprintf("Retrieved wrong block number for header hash %v -- requested %v but got %v", hash, num, header.Number.String()))
	}
	return header
}

type WavmInbox struct{}

func (i WavmInbox) PeekSequencerInbox() ([]byte, error) {
	pos := wavmio.GetInboxPosition()
	res := wavmio.ReadInboxMessage(pos)
	log.Info("PeekSequencerInbox", "pos", pos, "res[:8]", res[:8])
	return res, nil
}

func (i WavmInbox) GetSequencerInboxPosition() uint64 {
	pos := wavmio.GetInboxPosition()
	log.Info("GetSequencerInboxPosition", "pos", pos)
	return pos
}

func (i WavmInbox) AdvanceSequencerInbox() {
	log.Info("AdvanceSequencerInbox")
	wavmio.AdvanceInboxMessage()
}

func (i WavmInbox) GetPositionWithinMessage() uint64 {
	pos := wavmio.GetPositionWithinMessage()
	log.Info("GetPositionWithinMessage", "pos", pos)
	return pos
}

func (i WavmInbox) SetPositionWithinMessage(pos uint64) {
	log.Info("SetPositionWithinMessage", "pos", pos)
	wavmio.SetPositionWithinMessage(pos)
}

func (i WavmInbox) ReadDelayedInbox(seqNum uint64) (*METAostypes.L1IncomingMessage, error) {
	log.Info("ReadDelayedMsg", "seqNum", seqNum)
	data := wavmio.ReadDelayedInboxMessage(seqNum)
	return METAostypes.ParseIncomingL1Message(bytes.NewReader(data), func(batchNum uint64) ([]byte, error) {
		return wavmio.ReadInboxMessage(batchNum), nil
	})
}

type PreimageDASReader struct {
}

func (dasReader *PreimageDASReader) GetByHash(ctx context.Context, hash common.Hash) ([]byte, error) {
	oracle := func(hash common.Hash) ([]byte, error) {
		return wavmio.ResolveTypedPreimage(METAutil.Keccak256PreimageType, hash)
	}
	return dastree.Content(hash, oracle)
}

func (dasReader *PreimageDASReader) HealthCheck(ctx context.Context) error {
	return nil
}

func (dasReader *PreimageDASReader) ExpirationPolicy(ctx context.Context) (METAstate.ExpirationPolicy, error) {
	return METAstate.DiscardImmediately, nil
}

// To generate:
// key, _ := crypto.HexToECDSA("0000000000000000000000000000000000000000000000000000000000000001")
// sig, _ := crypto.Sign(make([]byte, 32), key)
// println(hex.EncodeToString(sig))
const sampleSignature = "a0b37f8fba683cc68f6574cd43b39f0343a50008bf6ccea9d13231d9e7e2e1e411edc8d307254296264aebfc3dc76cd8b668373a072fd64665b50000e9fcce5201"

// We call this early to populate the secp256k1 ecc basepoint cache in the cached early machine state.
// That means we don't need to re-compute it for every block.
func populateEcdsaCaches() {
	signature, err := hex.DecodeString(sampleSignature)
	if err != nil {
		log.Warn("failed to decode sample signature to populate ECDSA cache", "err", err)
		return
	}
	_, err = crypto.Ecrecover(make([]byte, 32), signature)
	if err != nil {
		log.Warn("failed to recover signature to populate ECDSA cache", "err", err)
		return
	}
}

func main() {
	wavmio.StubInit()
	gethhook.RequireHookedGeth()

	glogger := log.NewGlogHandler(log.StreamHandler(os.Stderr, log.TerminalFormat(false)))
	glogger.Verbosity(log.LvlError)
	log.Root().SetHandler(glogger)

	populateEcdsaCaches()

	raw := rawdb.NewDatabase(PreimageDb{})
	db := state.NewDatabase(raw)

	lastBlockHash := wavmio.GetLastBlockHash()

	var lastBlockHeader *types.Header
	var lastBlockStateRoot common.Hash
	if lastBlockHash != (common.Hash{}) {
		lastBlockHeader = getBlockHeaderByHash(lastBlockHash)
		lastBlockStateRoot = lastBlockHeader.Root
	}

	log.Info("Initial State", "lastBlockHash", lastBlockHash, "lastBlockStateRoot", lastBlockStateRoot)
	statedb, err := state.NewDeterministic(lastBlockStateRoot, db)
	if err != nil {
		panic(fmt.Sprintf("Error opening state db: %v", err.Error()))
	}

	readMessage := func(dasEnabled bool) *METAostypes.MessageWithMetadata {
		var delayedMessagesRead uint64
		if lastBlockHeader != nil {
			delayedMessagesRead = lastBlockHeader.Nonce.Uint64()
		}
		var dasReader METAstate.DataAvailabilityReader
		if dasEnabled {
			dasReader = &PreimageDASReader{}
		}
		backend := WavmInbox{}
		var keysetValidationMode = METAstate.KeysetPanicIfInvalid
		if backend.GetPositionWithinMessage() > 0 {
			keysetValidationMode = METAstate.KeysetDontValidate
		}
		inboxMultiplexer := METAstate.NewInboxMultiplexer(backend, delayedMessagesRead, dasReader, keysetValidationMode)
		ctx := context.Background()
		message, err := inboxMultiplexer.Pop(ctx)
		if err != nil {
			panic(fmt.Sprintf("Error reading from inbox multiplexer: %v", err.Error()))
		}

		return message
	}

	var newBlock *types.Block
	if lastBlockStateRoot != (common.Hash{}) {
		// METAOS has already been initialized.
		// Load the chain config and then produce a block normally.

		initialMETAosState, err := METAosState.OpenSystemMETAosState(statedb, nil, true)
		if err != nil {
			panic(fmt.Sprintf("Error opening initial METAOS state: %v", err.Error()))
		}
		chainId, err := initialMETAosState.ChainId()
		if err != nil {
			panic(fmt.Sprintf("Error getting chain ID from initial METAOS state: %v", err.Error()))
		}
		genesisBlockNum, err := initialMETAosState.GenesisBlockNum()
		if err != nil {
			panic(fmt.Sprintf("Error getting genesis block number from initial METAOS state: %v", err.Error()))
		}
		chainConfigJson, err := initialMETAosState.ChainConfig()
		if err != nil {
			panic(fmt.Sprintf("Error getting chain config from initial METAOS state: %v", err.Error()))
		}
		var chainConfig *params.ChainConfig
		if len(chainConfigJson) > 0 {
			chainConfig = &params.ChainConfig{}
			err = json.Unmarshal(chainConfigJson, chainConfig)
			if err != nil {
				panic(fmt.Sprintf("Error parsing chain config: %v", err.Error()))
			}
			if chainConfig.ChainID.Cmp(chainId) != 0 {
				panic(fmt.Sprintf("Error: chain id mismatch, chainID: %v, chainConfig.ChainID: %v", chainId, chainConfig.ChainID))
			}
			if chainConfig.metachainChainParams.GenesisBlockNum != genesisBlockNum {
				panic(fmt.Sprintf("Error: genesis block number mismatch, genesisBlockNum: %v, chainConfig.metachainParams.GenesisBlockNum: %v", genesisBlockNum, chainConfig.metachainChainParams.GenesisBlockNum))
			}
		} else {
			log.Info("Falling back to hardcoded chain config.")
			chainConfig, err = chaininfo.GetChainConfig(chainId, "", genesisBlockNum, []string{}, "")
			if err != nil {
				panic(err)
			}
		}

		message := readMessage(chainConfig.metachainChainParams.DataAvailabilityCommittee)

		chainContext := WavmChainContext{}
		batchFetcher := func(batchNum uint64) ([]byte, error) {
			return wavmio.ReadInboxMessage(batchNum), nil
		}
		newBlock, _, err = METAos.ProduceBlock(message.Message, message.DelayedMessagesRead, lastBlockHeader, statedb, chainContext, chainConfig, batchFetcher)
		if err != nil {
			panic(err)
		}

	} else {
		// Initialize METAOS with this init message and create the genesis block.

		message := readMessage(false)

		initMessage, err := message.Message.ParseInitMessage()
		if err != nil {
			panic(err)
		}
		chainConfig := initMessage.ChainConfig
		if chainConfig == nil {
			log.Info("No chain config in the init message. Falling back to hardcoded chain config.")
			chainConfig, err = chaininfo.GetChainConfig(initMessage.ChainId, "", 0, []string{}, "")
			if err != nil {
				panic(err)
			}
		}

		_, err = METAosState.InitializeMETAosState(statedb, burn.NewSystemBurner(nil, false), chainConfig, initMessage)
		if err != nil {
			panic(fmt.Sprintf("Error initializing METAOS: %v", err.Error()))
		}

		newBlock = METAosState.MakeGenesisBlock(common.Hash{}, 0, 0, statedb.IntermediateRoot(true), chainConfig)

	}

	newBlockHash := newBlock.Hash()

	log.Info("Final State", "newBlockHash", newBlockHash, "StateRoot", newBlock.Root())

	extraInfo := types.DeserializeHeaderExtraInformation(newBlock.Header())
	if extraInfo.METAOSFormatVersion == 0 {
		panic(fmt.Sprintf("Error deserializing header extra info: %+v", newBlock.Header()))
	}
	wavmio.SetLastBlockHash(newBlockHash)
	wavmio.SetSendRoot(extraInfo.SendRoot)

	wavmio.StubFinal()
}
