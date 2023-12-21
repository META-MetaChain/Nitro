// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

package METAtest

import (
	"context"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/META-MetaChain/nitro/METAnode"
	"github.com/META-MetaChain/nitro/METAos"
	"github.com/META-MetaChain/nitro/METAos/METAostypes"
	"github.com/META-MetaChain/nitro/METAos/l2pricing"
	"github.com/META-MetaChain/nitro/METAos/retryables"
	"github.com/META-MetaChain/nitro/METAos/util"
	"github.com/META-MetaChain/nitro/execution/gethexec"

	"github.com/META-MetaChain/nitro/solgen/go/bridgegen"
	"github.com/META-MetaChain/nitro/solgen/go/mocksgen"
	"github.com/META-MetaChain/nitro/solgen/go/node_interfacegen"
	"github.com/META-MetaChain/nitro/solgen/go/precompilesgen"
	"github.com/META-MetaChain/nitro/util/METAmath"
	"github.com/META-MetaChain/nitro/util/colors"
)

func retryableSetup(t *testing.T) (
	*NodeBuilder,
	*bridgegen.Inbox,
	func(*types.Receipt) *types.Transaction,
	context.Context,
	func(),
) {
	ctx, cancel := context.WithCancel(context.Background())
	builder := NewNodeBuilder(ctx).DefaultConfig(t, true)
	builder.Build(t)

	builder.L2Info.GenerateAccount("User2")
	builder.L2Info.GenerateAccount("Beneficiary")
	builder.L2Info.GenerateAccount("Burn")

	delayedInbox, err := bridgegen.NewInbox(builder.L1Info.GetAddress("Inbox"), builder.L1.Client)
	Require(t, err)
	delayedBridge, err := METAnode.NewDelayedBridge(builder.L1.Client, builder.L1Info.GetAddress("Bridge"), 0)
	Require(t, err)

	lookupL2Tx := func(l1Receipt *types.Receipt) *types.Transaction {
		messages, err := delayedBridge.LookupMessagesInRange(ctx, l1Receipt.BlockNumber, l1Receipt.BlockNumber, nil)
		Require(t, err)
		if len(messages) == 0 {
			Fatal(t, "didn't find message for submission")
		}
		var submissionTxs []*types.Transaction
		msgTypes := map[uint8]bool{
			METAostypes.L1MessageType_SubmitRetryable: true,
			METAostypes.L1MessageType_EthDeposit:      true,
			METAostypes.L1MessageType_L2Message:       true,
		}
		txTypes := map[uint8]bool{
			types.metachainSubmitRetryableTxType: true,
			types.metachainDepositTxType:         true,
			types.metachainContractTxType:        true,
		}
		for _, message := range messages {
			if !msgTypes[message.Message.Header.Kind] {
				continue
			}
			txs, err := METAos.ParseL2Transactions(message.Message, params.metachainDevTestChainConfig().ChainID, nil)
			Require(t, err)
			for _, tx := range txs {
				if txTypes[tx.Type()] {
					submissionTxs = append(submissionTxs, tx)
				}
			}
		}
		if len(submissionTxs) != 1 {
			Fatal(t, "expected 1 tx from submission, found", len(submissionTxs))
		}
		return submissionTxs[0]
	}

	// burn some gas so that the faucet's Callvalue + Balance never exceeds a uint256
	discard := METAmath.BigMul(big.NewInt(1e12), big.NewInt(1e12))
	builder.L2.TransferBalance(t, "Faucet", "Burn", discard, builder.L2Info)

	teardown := func() {

		// check the integrity of the RPC
		blockNum, err := builder.L2.Client.BlockNumber(ctx)
		Require(t, err, "failed to get L2 block number")
		for number := uint64(0); number < blockNum; number++ {
			block, err := builder.L2.Client.BlockByNumber(ctx, METAmath.UintToBig(number))
			Require(t, err, "failed to get L2 block", number, "of", blockNum)
			if block.Number().Uint64() != number {
				Fatal(t, "block number mismatch", number, block.Number().Uint64())
			}
		}

		cancel()

		builder.L2.ConsensusNode.StopAndWait()
		requireClose(t, builder.L1.Stack)
	}
	return builder, delayedInbox, lookupL2Tx, ctx, teardown
}

func TestRetryableNoExist(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	builder := NewNodeBuilder(ctx).DefaultConfig(t, false)
	cleanup := builder.Build(t)
	defer cleanup()

	METARetryableTx, err := precompilesgen.NewMETARetryableTx(common.HexToAddress("6e"), builder.L2.Client)
	Require(t, err)
	_, err = METARetryableTx.GetTimeout(&bind.CallOpts{}, common.Hash{})
	if err.Error() != "execution reverted: error NoTicketWithID()" {
		Fatal(t, "didn't get expected NoTicketWithID error")
	}
}

func TestSubmitRetryableImmediateSuccess(t *testing.T) {
	t.Parallel()
	builder, delayedInbox, lookupL2Tx, ctx, teardown := retryableSetup(t)
	defer teardown()

	user2Address := builder.L2Info.GetAddress("User2")
	beneficiaryAddress := builder.L2Info.GetAddress("Beneficiary")

	deposit := METAmath.BigMul(big.NewInt(1e12), big.NewInt(1e12))
	callValue := big.NewInt(1e6)

	nodeInterface, err := node_interfacegen.NewNodeInterface(types.NodeInterfaceAddress, builder.L2.Client)
	Require(t, err, "failed to deploy NodeInterface")

	// estimate the gas needed to auto redeem the retryable
	usertxoptsL2 := builder.L2Info.GetDefaultTransactOpts("Faucet", ctx)
	usertxoptsL2.NoSend = true
	usertxoptsL2.GasMargin = 0
	tx, err := nodeInterface.EstimateRetryableTicket(
		&usertxoptsL2,
		usertxoptsL2.From,
		deposit,
		user2Address,
		callValue,
		beneficiaryAddress,
		beneficiaryAddress,
		[]byte{0x32, 0x42, 0x32, 0x88}, // increase the cost to beyond that of params.TxGas
	)
	Require(t, err, "failed to estimate retryable submission")
	estimate := tx.Gas()
	colors.PrintBlue("estimate: ", estimate)

	// submit & auto redeem the retryable using the gas estimate
	usertxoptsL1 := builder.L1Info.GetDefaultTransactOpts("Faucet", ctx)
	usertxoptsL1.Value = deposit
	l1tx, err := delayedInbox.CreateRetryableTicket(
		&usertxoptsL1,
		user2Address,
		callValue,
		big.NewInt(1e16),
		beneficiaryAddress,
		beneficiaryAddress,
		METAmath.UintToBig(estimate),
		big.NewInt(l2pricing.InitialBaseFeeWei*2),
		[]byte{0x32, 0x42, 0x32, 0x88},
	)
	Require(t, err)

	l1Receipt, err := builder.L1.EnsureTxSucceeded(l1tx)
	Require(t, err)
	if l1Receipt.Status != types.ReceiptStatusSuccessful {
		Fatal(t, "l1Receipt indicated failure")
	}

	waitForL1DelayBlocks(t, ctx, builder)

	receipt, err := builder.L2.EnsureTxSucceeded(lookupL2Tx(l1Receipt))
	Require(t, err)
	if receipt.Status != types.ReceiptStatusSuccessful {
		Fatal(t)
	}

	l2balance, err := builder.L2.Client.BalanceAt(ctx, builder.L2Info.GetAddress("User2"), nil)
	Require(t, err)

	if !METAmath.BigEquals(l2balance, callValue) {
		Fatal(t, "Unexpected balance:", l2balance)
	}
}

func TestSubmitRetryableEmptyEscrow(t *testing.T) {
	t.Parallel()
	builder, delayedInbox, lookupL2Tx, ctx, teardown := retryableSetup(t)
	defer teardown()

	user2Address := builder.L2Info.GetAddress("User2")
	beneficiaryAddress := builder.L2Info.GetAddress("Beneficiary")

	deposit := METAmath.BigMul(big.NewInt(1e12), big.NewInt(1e12))
	callValue := common.Big0

	nodeInterface, err := node_interfacegen.NewNodeInterface(types.NodeInterfaceAddress, builder.L2.Client)
	Require(t, err, "failed to deploy NodeInterface")

	// estimate the gas needed to auto redeem the retryable
	usertxoptsL2 := builder.L2Info.GetDefaultTransactOpts("Faucet", ctx)
	usertxoptsL2.NoSend = true
	usertxoptsL2.GasMargin = 0
	tx, err := nodeInterface.EstimateRetryableTicket(
		&usertxoptsL2,
		usertxoptsL2.From,
		deposit,
		user2Address,
		callValue,
		beneficiaryAddress,
		beneficiaryAddress,
		[]byte{0x32, 0x42, 0x32, 0x88}, // increase the cost to beyond that of params.TxGas
	)
	Require(t, err, "failed to estimate retryable submission")
	estimate := tx.Gas()
	colors.PrintBlue("estimate: ", estimate)

	// submit & auto redeem the retryable using the gas estimate
	usertxoptsL1 := builder.L1Info.GetDefaultTransactOpts("Faucet", ctx)
	usertxoptsL1.Value = deposit
	l1tx, err := delayedInbox.CreateRetryableTicket(
		&usertxoptsL1,
		user2Address,
		callValue,
		big.NewInt(1e16),
		beneficiaryAddress,
		beneficiaryAddress,
		METAmath.UintToBig(estimate),
		big.NewInt(l2pricing.InitialBaseFeeWei*2),
		[]byte{0x32, 0x42, 0x32, 0x88},
	)
	Require(t, err)

	l1Receipt, err := builder.L1.EnsureTxSucceeded(l1tx)
	Require(t, err)
	if l1Receipt.Status != types.ReceiptStatusSuccessful {
		Fatal(t, "l1Receipt indicated failure")
	}

	waitForL1DelayBlocks(t, ctx, builder)

	l2Tx := lookupL2Tx(l1Receipt)
	receipt, err := builder.L2.EnsureTxSucceeded(l2Tx)
	Require(t, err)
	if receipt.Status != types.ReceiptStatusSuccessful {
		Fatal(t)
	}

	l2balance, err := builder.L2.Client.BalanceAt(ctx, builder.L2Info.GetAddress("User2"), nil)
	Require(t, err)

	if !METAmath.BigEquals(l2balance, callValue) {
		Fatal(t, "Unexpected balance:", l2balance)
	}

	escrowAccount := retryables.RetryableEscrowAddress(l2Tx.Hash())
	state, err := builder.L2.ExecNode.METAInterface.BlockChain().State()
	Require(t, err)
	escrowCodeHash := state.GetCodeHash(escrowAccount)
	if escrowCodeHash == (common.Hash{}) {
		Fatal(t, "Escrow account deleted (or not created)")
	} else if escrowCodeHash != types.EmptyCodeHash {
		Fatal(t, "Escrow account has unexpected code hash", escrowCodeHash)
	}
}

func TestSubmitRetryableFailThenRetry(t *testing.T) {
	t.Parallel()
	builder, delayedInbox, lookupL2Tx, ctx, teardown := retryableSetup(t)
	defer teardown()

	ownerTxOpts := builder.L2Info.GetDefaultTransactOpts("Owner", ctx)
	usertxopts := builder.L1Info.GetDefaultTransactOpts("Faucet", ctx)
	usertxopts.Value = METAmath.BigMul(big.NewInt(1e12), big.NewInt(1e12))

	simpleAddr, simple := builder.L2.DeploySimple(t, ownerTxOpts)
	simpleABI, err := mocksgen.SimpleMetaData.GetAbi()
	Require(t, err)

	beneficiaryAddress := builder.L2Info.GetAddress("Beneficiary")
	l1tx, err := delayedInbox.CreateRetryableTicket(
		&usertxopts,
		simpleAddr,
		common.Big0,
		big.NewInt(1e16),
		beneficiaryAddress,
		beneficiaryAddress,
		// send enough L2 gas for intrinsic but not compute
		big.NewInt(int64(params.TxGas+params.TxDataNonZeroGasEIP2028*4)),
		big.NewInt(l2pricing.InitialBaseFeeWei*2),
		simpleABI.Methods["incrementRedeem"].ID,
	)
	Require(t, err)

	l1Receipt, err := builder.L1.EnsureTxSucceeded(l1tx)
	Require(t, err)
	if l1Receipt.Status != types.ReceiptStatusSuccessful {
		Fatal(t, "l1Receipt indicated failure")
	}

	waitForL1DelayBlocks(t, ctx, builder)

	receipt, err := builder.L2.EnsureTxSucceeded(lookupL2Tx(l1Receipt))
	Require(t, err)
	if len(receipt.Logs) != 2 {
		Fatal(t, len(receipt.Logs))
	}
	ticketId := receipt.Logs[0].Topics[1]
	firstRetryTxId := receipt.Logs[1].Topics[2]

	// get receipt for the auto redeem, make sure it failed
	receipt, err = WaitForTx(ctx, builder.L2.Client, firstRetryTxId, time.Second*5)
	Require(t, err)
	if receipt.Status != types.ReceiptStatusFailed {
		Fatal(t, receipt.GasUsed)
	}

	METARetryableTx, err := precompilesgen.NewMETARetryableTx(common.HexToAddress("6e"), builder.L2.Client)
	Require(t, err)
	tx, err := METARetryableTx.Redeem(&ownerTxOpts, ticketId)
	Require(t, err)
	receipt, err = builder.L2.EnsureTxSucceeded(tx)
	Require(t, err)

	retryTxId := receipt.Logs[0].Topics[2]

	// check the receipt for the retry
	receipt, err = WaitForTx(ctx, builder.L2.Client, retryTxId, time.Second*1)
	Require(t, err)
	if receipt.Status != types.ReceiptStatusSuccessful {
		Fatal(t, receipt.Status)
	}

	// verify that the increment happened, so we know the retry succeeded
	counter, err := simple.Counter(&bind.CallOpts{})
	Require(t, err)

	if counter != 1 {
		Fatal(t, "Unexpected counter:", counter)
	}

	if len(receipt.Logs) != 1 {
		Fatal(t, "Unexpected log count:", len(receipt.Logs))
	}
	parsed, err := simple.ParseRedeemedEvent(*receipt.Logs[0])
	Require(t, err)
	aliasedSender := util.RemapL1Address(usertxopts.From)
	if parsed.Caller != aliasedSender {
		Fatal(t, "Unexpected caller", parsed.Caller, "expected", aliasedSender)
	}
	if parsed.Redeemer != ownerTxOpts.From {
		Fatal(t, "Unexpected redeemer", parsed.Redeemer, "expected", ownerTxOpts.From)
	}
}

func TestSubmissionGasCosts(t *testing.T) {
	t.Parallel()
	builder, delayedInbox, lookupL2Tx, ctx, teardown := retryableSetup(t)
	defer teardown()
	infraFeeAddr, networkFeeAddr := setupFeeAddresses(t, ctx, builder)
	elevateL2Basefee(t, ctx, builder)

	usertxopts := builder.L1Info.GetDefaultTransactOpts("Faucet", ctx)
	usertxopts.Value = METAmath.BigMul(big.NewInt(1e12), big.NewInt(1e12))

	builder.L2Info.GenerateAccount("Refund")
	builder.L2Info.GenerateAccount("Receive")
	faucetAddress := util.RemapL1Address(builder.L1Info.GetAddress("Faucet"))
	beneficiaryAddress := builder.L2Info.GetAddress("Beneficiary")
	feeRefundAddress := builder.L2Info.GetAddress("Refund")
	receiveAddress := builder.L2Info.GetAddress("Receive")

	colors.PrintBlue("Faucet      ", faucetAddress)
	colors.PrintBlue("Receive     ", receiveAddress)
	colors.PrintBlue("Beneficiary ", beneficiaryAddress)
	colors.PrintBlue("Fee Refund  ", feeRefundAddress)

	fundsBeforeSubmit, err := builder.L2.Client.BalanceAt(ctx, faucetAddress, nil)
	Require(t, err)

	infraBalanceBefore, err := builder.L2.Client.BalanceAt(ctx, infraFeeAddr, nil)
	Require(t, err)
	networkBalanceBefore, err := builder.L2.Client.BalanceAt(ctx, networkFeeAddr, nil)
	Require(t, err)

	usefulGas := params.TxGas
	excessGasLimit := uint64(808)

	maxSubmissionFee := big.NewInt(1e14)
	retryableGas := METAmath.UintToBig(usefulGas + excessGasLimit) // will only burn the intrinsic cost
	retryableL2CallValue := big.NewInt(1e4)
	retryableCallData := []byte{}
	gasFeeCap := big.NewInt(l2pricing.InitialBaseFeeWei * 2)
	l1tx, err := delayedInbox.CreateRetryableTicket(
		&usertxopts,
		receiveAddress,
		retryableL2CallValue,
		maxSubmissionFee,
		feeRefundAddress,
		beneficiaryAddress,
		retryableGas,
		gasFeeCap,
		retryableCallData,
	)
	Require(t, err)

	l1Receipt, err := builder.L1.EnsureTxSucceeded(l1tx)
	Require(t, err)
	if l1Receipt.Status != types.ReceiptStatusSuccessful {
		Fatal(t, "l1Receipt indicated failure")
	}

	waitForL1DelayBlocks(t, ctx, builder)

	submissionTxOuter := lookupL2Tx(l1Receipt)
	submissionReceipt, err := builder.L2.EnsureTxSucceeded(submissionTxOuter)
	Require(t, err)
	if len(submissionReceipt.Logs) != 2 {
		Fatal(t, "Unexpected number of logs:", len(submissionReceipt.Logs))
	}
	firstRetryTxId := submissionReceipt.Logs[1].Topics[2]
	// get receipt for the auto redeem
	redeemReceipt, err := WaitForTx(ctx, builder.L2.Client, firstRetryTxId, time.Second*5)
	Require(t, err)
	if redeemReceipt.Status != types.ReceiptStatusSuccessful {
		Fatal(t, "first retry tx failed")
	}
	redeemBlock, err := builder.L2.Client.HeaderByNumber(ctx, redeemReceipt.BlockNumber)
	Require(t, err)

	l2BaseFee := redeemBlock.BaseFee
	excessGasPrice := METAmath.BigSub(gasFeeCap, l2BaseFee)
	excessWei := METAmath.BigMulByUint(l2BaseFee, excessGasLimit)
	excessWei.Add(excessWei, METAmath.BigMul(excessGasPrice, retryableGas))

	fundsAfterSubmit, err := builder.L2.Client.BalanceAt(ctx, faucetAddress, nil)
	Require(t, err)
	beneficiaryFunds, err := builder.L2.Client.BalanceAt(ctx, beneficiaryAddress, nil)
	Require(t, err)
	refundFunds, err := builder.L2.Client.BalanceAt(ctx, feeRefundAddress, nil)
	Require(t, err)
	receiveFunds, err := builder.L2.Client.BalanceAt(ctx, receiveAddress, nil)
	Require(t, err)

	infraBalanceAfter, err := builder.L2.Client.BalanceAt(ctx, infraFeeAddr, nil)
	Require(t, err)
	networkBalanceAfter, err := builder.L2.Client.BalanceAt(ctx, networkFeeAddr, nil)
	Require(t, err)

	colors.PrintBlue("CallGas    ", retryableGas)
	colors.PrintMint("Gas cost   ", METAmath.BigMul(retryableGas, l2BaseFee))
	colors.PrintBlue("Payment    ", usertxopts.Value)

	colors.PrintMint("Faucet before ", fundsAfterSubmit)
	colors.PrintMint("Faucet after  ", fundsAfterSubmit)

	// the retryable should pay the receiver the supplied callvalue
	colors.PrintMint("Receive       ", receiveFunds)
	colors.PrintBlue("L2 Call Value ", retryableL2CallValue)
	if !METAmath.BigEquals(receiveFunds, retryableL2CallValue) {
		Fatal(t, "Recipient didn't receive the right funds")
	}

	// the beneficiary should receive nothing
	colors.PrintMint("Beneficiary   ", beneficiaryFunds)
	if beneficiaryFunds.Sign() != 0 {
		Fatal(t, "The beneficiary shouldn't have received funds")
	}

	// the fee refund address should recieve the excess gas
	colors.PrintBlue("Base Fee         ", l2BaseFee)
	colors.PrintBlue("Excess Gas Price ", excessGasPrice)
	colors.PrintBlue("Excess Gas       ", excessGasLimit)
	colors.PrintBlue("Excess Wei       ", excessWei)
	colors.PrintMint("Fee Refund       ", refundFunds)
	if !METAmath.BigEquals(refundFunds, METAmath.BigAdd(excessWei, maxSubmissionFee)) {
		Fatal(t, "The Fee Refund Address didn't receive the right funds")
	}

	// the faucet must pay for both the gas used and the call value supplied
	expectedGasChange := METAmath.BigMul(gasFeeCap, retryableGas)
	expectedGasChange = METAmath.BigSub(expectedGasChange, usertxopts.Value) // the user is credited this
	expectedGasChange = METAmath.BigAdd(expectedGasChange, maxSubmissionFee)
	expectedGasChange = METAmath.BigAdd(expectedGasChange, retryableL2CallValue)

	if !METAmath.BigEquals(fundsBeforeSubmit, METAmath.BigAdd(fundsAfterSubmit, expectedGasChange)) {
		diff := METAmath.BigSub(fundsBeforeSubmit, fundsAfterSubmit)
		colors.PrintRed("Expected ", expectedGasChange)
		colors.PrintRed("Observed ", diff)
		colors.PrintRed("Off by   ", METAmath.BigSub(expectedGasChange, diff))
		Fatal(t, "Supplied gas was improperly deducted\n", fundsBeforeSubmit, "\n", fundsAfterSubmit)
	}

	METAGasInfo, err := precompilesgen.NewMETAGasInfo(common.HexToAddress("0x6c"), builder.L2.Client)
	Require(t, err)
	minimumBaseFee, err := METAGasInfo.GetMinimumGasPrice(&bind.CallOpts{Context: ctx})
	Require(t, err)

	expectedFee := METAmath.BigMulByUint(l2BaseFee, usefulGas)
	expectedInfraFee := METAmath.BigMulByUint(minimumBaseFee, usefulGas)
	expectedNetworkFee := METAmath.BigSub(expectedFee, expectedInfraFee)

	infraFee := METAmath.BigSub(infraBalanceAfter, infraBalanceBefore)
	networkFee := METAmath.BigSub(networkBalanceAfter, networkBalanceBefore)
	fee := METAmath.BigAdd(infraFee, networkFee)

	colors.PrintMint("paid infra fee:      ", infraFee)
	colors.PrintMint("paid network fee:    ", networkFee)
	colors.PrintMint("paid fee:            ", fee)

	if !METAmath.BigEquals(infraFee, expectedInfraFee) {
		Fatal(t, "Unexpected infra fee paid, want:", expectedInfraFee, "have:", infraFee)
	}
	if !METAmath.BigEquals(networkFee, expectedNetworkFee) {
		Fatal(t, "Unexpected network fee paid, want:", expectedNetworkFee, "have:", networkFee)
	}
}

func waitForL1DelayBlocks(t *testing.T, ctx context.Context, builder *NodeBuilder) {
	// sending l1 messages creates l1 blocks.. make enough to get that delayed inbox message in
	for i := 0; i < 30; i++ {
		builder.L1.SendWaitTestTransactions(t, []*types.Transaction{
			builder.L1Info.PrepareTx("Faucet", "User", 30000, big.NewInt(1e12), nil),
		})
	}
}

func TestDepositETH(t *testing.T) {
	t.Parallel()
	builder, delayedInbox, lookupL2Tx, ctx, teardown := retryableSetup(t)
	defer teardown()

	faucetAddr := builder.L1Info.GetAddress("Faucet")

	oldBalance, err := builder.L2.Client.BalanceAt(ctx, faucetAddr, nil)
	if err != nil {
		t.Fatalf("BalanceAt(%v) unexpected error: %v", faucetAddr, err)
	}

	txOpts := builder.L1Info.GetDefaultTransactOpts("Faucet", ctx)
	txOpts.Value = big.NewInt(13)

	l1tx, err := delayedInbox.DepositEth0(&txOpts)
	if err != nil {
		t.Fatalf("DepositEth0() unexected error: %v", err)
	}

	l1Receipt, err := builder.L1.EnsureTxSucceeded(l1tx)
	if err != nil {
		t.Fatalf("EnsureTxSucceeded() unexpected error: %v", err)
	}
	if l1Receipt.Status != types.ReceiptStatusSuccessful {
		t.Errorf("Got transaction status: %v, want: %v", l1Receipt.Status, types.ReceiptStatusSuccessful)
	}
	waitForL1DelayBlocks(t, ctx, builder)

	l2Receipt, err := builder.L2.EnsureTxSucceeded(lookupL2Tx(l1Receipt))
	if err != nil {
		t.Fatalf("EnsureTxSucceeded unexpected error: %v", err)
	}
	newBalance, err := builder.L2.Client.BalanceAt(ctx, faucetAddr, l2Receipt.BlockNumber)
	if err != nil {
		t.Fatalf("BalanceAt(%v) unexpected error: %v", faucetAddr, err)
	}
	if got := new(big.Int); got.Sub(newBalance, oldBalance).Cmp(txOpts.Value) != 0 {
		t.Errorf("Got transferred: %v, want: %v", got, txOpts.Value)
	}
}

func TestmetachainContractTx(t *testing.T) {
	builder, delayedInbox, lookupL2Tx, ctx, teardown := retryableSetup(t)
	defer teardown()
	faucetL2Addr := util.RemapL1Address(builder.L1Info.GetAddress("Faucet"))
	builder.L2.TransferBalanceTo(t, "Faucet", faucetL2Addr, big.NewInt(1e18), builder.L2Info)

	l2TxOpts := builder.L2Info.GetDefaultTransactOpts("Faucet", ctx)
	l2ContractAddr, _ := builder.L2.DeploySimple(t, l2TxOpts)
	l2ContractABI, err := abi.JSON(strings.NewReader(mocksgen.SimpleABI))
	if err != nil {
		t.Fatalf("Error parsing contract ABI: %v", err)
	}
	data, err := l2ContractABI.Pack("checkCalls", true, true, false, false, false, false)
	if err != nil {
		t.Fatalf("Error packing method's call data: %v", err)
	}
	unsignedTx := types.NewTx(&types.metachainContractTx{
		ChainId:   builder.L2Info.Signer.ChainID(),
		From:      faucetL2Addr,
		GasFeeCap: builder.L2Info.GasPrice.Mul(builder.L2Info.GasPrice, big.NewInt(2)),
		Gas:       1e6,
		To:        &l2ContractAddr,
		Value:     common.Big0,
		Data:      data,
	})
	txOpts := builder.L1Info.GetDefaultTransactOpts("Faucet", ctx)
	l1tx, err := delayedInbox.SendContractTransaction(
		&txOpts,
		METAmath.UintToBig(unsignedTx.Gas()),
		unsignedTx.GasFeeCap(),
		*unsignedTx.To(),
		unsignedTx.Value(),
		unsignedTx.Data(),
	)
	if err != nil {
		t.Fatalf("Error sending unsigned transaction: %v", err)
	}
	receipt, err := builder.L1.EnsureTxSucceeded(l1tx)
	if err != nil {
		t.Fatalf("EnsureTxSucceeded(%v) unexpected error: %v", l1tx.Hash(), err)
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		t.Errorf("L1 transaction: %v has failed", l1tx.Hash())
	}
	waitForL1DelayBlocks(t, ctx, builder)
	_, err = builder.L2.EnsureTxSucceeded(lookupL2Tx(receipt))
	if err != nil {
		t.Fatalf("EnsureTxSucceeded(%v) unexpected error: %v", unsignedTx.Hash(), err)
	}
}

func TestL1FundedUnsignedTransaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	builder := NewNodeBuilder(ctx).DefaultConfig(t, true)
	cleanup := builder.Build(t)
	defer cleanup()

	faucetL2Addr := util.RemapL1Address(builder.L1Info.GetAddress("Faucet"))
	// Transfer balance to Faucet's corresponding L2 address, so that there is
	// enough balance on its' account for executing L2 transaction.
	builder.L2.TransferBalanceTo(t, "Faucet", faucetL2Addr, big.NewInt(1e18), builder.L2Info)

	l2TxOpts := builder.L2Info.GetDefaultTransactOpts("Faucet", ctx)
	contractAddr, _ := builder.L2.DeploySimple(t, l2TxOpts)
	contractABI, err := abi.JSON(strings.NewReader(mocksgen.SimpleABI))
	if err != nil {
		t.Fatalf("Error parsing contract ABI: %v", err)
	}
	data, err := contractABI.Pack("checkCalls", true, true, false, false, false, false)
	if err != nil {
		t.Fatalf("Error packing method's call data: %v", err)
	}
	nonce, err := builder.L2.Client.NonceAt(ctx, faucetL2Addr, nil)
	if err != nil {
		t.Fatalf("Error getting nonce at address: %v, error: %v", faucetL2Addr, err)
	}
	unsignedTx := types.NewTx(&types.metachainUnsignedTx{
		ChainId:   builder.L2Info.Signer.ChainID(),
		From:      faucetL2Addr,
		Nonce:     nonce,
		GasFeeCap: builder.L2Info.GasPrice,
		Gas:       1e6,
		To:        &contractAddr,
		Value:     common.Big0,
		Data:      data,
	})

	delayedInbox, err := bridgegen.NewInbox(builder.L1Info.GetAddress("Inbox"), builder.L1.Client)
	if err != nil {
		t.Fatalf("Error getting Go binding of L1 Inbox contract: %v", err)
	}

	txOpts := builder.L1Info.GetDefaultTransactOpts("Faucet", ctx)
	l1tx, err := delayedInbox.SendUnsignedTransaction(
		&txOpts,
		METAmath.UintToBig(unsignedTx.Gas()),
		unsignedTx.GasFeeCap(),
		METAmath.UintToBig(unsignedTx.Nonce()),
		*unsignedTx.To(),
		unsignedTx.Value(),
		unsignedTx.Data(),
	)
	if err != nil {
		t.Fatalf("Error sending unsigned transaction: %v", err)
	}
	receipt, err := builder.L1.EnsureTxSucceeded(l1tx)
	if err != nil {
		t.Fatalf("EnsureTxSucceeded(%v) unexpected error: %v", l1tx.Hash(), err)
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		t.Errorf("L1 transaction: %v has failed", l1tx.Hash())
	}
	waitForL1DelayBlocks(t, ctx, builder)
	receipt, err = builder.L2.EnsureTxSucceeded(unsignedTx)
	if err != nil {
		t.Fatalf("EnsureTxSucceeded(%v) unexpected error: %v", unsignedTx.Hash(), err)
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		t.Errorf("L2 transaction: %v has failed", receipt.TxHash)
	}
}

func TestRetryableSubmissionAndRedeemFees(t *testing.T) {
	builder, delayedInbox, lookupL2Tx, ctx, teardown := retryableSetup(t)
	defer teardown()
	infraFeeAddr, networkFeeAddr := setupFeeAddresses(t, ctx, builder)

	ownerTxOpts := builder.L2Info.GetDefaultTransactOpts("Owner", ctx)
	simpleAddr, simple := builder.L2.DeploySimple(t, ownerTxOpts)
	simpleABI, err := mocksgen.SimpleMetaData.GetAbi()
	Require(t, err)

	elevateL2Basefee(t, ctx, builder)

	infraBalanceBefore, err := builder.L2.Client.BalanceAt(ctx, infraFeeAddr, nil)
	Require(t, err)
	networkBalanceBefore, err := builder.L2.Client.BalanceAt(ctx, networkFeeAddr, nil)
	Require(t, err)

	beneficiaryAddress := builder.L2Info.GetAddress("Beneficiary")
	deposit := METAmath.BigMul(big.NewInt(1e12), big.NewInt(1e12))
	callValue := common.Big0
	usertxoptsL1 := builder.L1Info.GetDefaultTransactOpts("Faucet", ctx)
	usertxoptsL1.Value = deposit
	baseFee := builder.L2.GetBaseFee(t)
	l1tx, err := delayedInbox.CreateRetryableTicket(
		&usertxoptsL1,
		simpleAddr,
		callValue,
		big.NewInt(1e16),
		beneficiaryAddress,
		beneficiaryAddress,
		// send enough L2 gas for intrinsic but not compute
		big.NewInt(int64(params.TxGas+params.TxDataNonZeroGasEIP2028*4)),
		big.NewInt(baseFee.Int64()*2),
		simpleABI.Methods["incrementRedeem"].ID,
	)
	Require(t, err)
	l1Receipt, err := builder.L1.EnsureTxSucceeded(l1tx)
	Require(t, err)
	if l1Receipt.Status != types.ReceiptStatusSuccessful {
		Fatal(t, "l1Receipt indicated failure")
	}

	waitForL1DelayBlocks(t, ctx, builder)

	submissionTxOuter := lookupL2Tx(l1Receipt)
	submissionReceipt, err := builder.L2.EnsureTxSucceeded(submissionTxOuter)
	Require(t, err)
	if len(submissionReceipt.Logs) != 2 {
		Fatal(t, len(submissionReceipt.Logs))
	}
	ticketId := submissionReceipt.Logs[0].Topics[1]
	firstRetryTxId := submissionReceipt.Logs[1].Topics[2]
	// get receipt for the auto redeem, make sure it failed
	autoRedeemReceipt, err := WaitForTx(ctx, builder.L2.Client, firstRetryTxId, time.Second*5)
	Require(t, err)
	if autoRedeemReceipt.Status != types.ReceiptStatusFailed {
		Fatal(t, "first retry tx shouldn't have succeeded")
	}

	infraBalanceAfterSubmission, err := builder.L2.Client.BalanceAt(ctx, infraFeeAddr, nil)
	Require(t, err)
	networkBalanceAfterSubmission, err := builder.L2.Client.BalanceAt(ctx, networkFeeAddr, nil)
	Require(t, err)

	usertxoptsL2 := builder.L2Info.GetDefaultTransactOpts("Faucet", ctx)
	METARetryableTx, err := precompilesgen.NewMETARetryableTx(common.HexToAddress("6e"), builder.L2.Client)
	Require(t, err)
	tx, err := METARetryableTx.Redeem(&usertxoptsL2, ticketId)
	Require(t, err)
	redeemReceipt, err := builder.L2.EnsureTxSucceeded(tx)
	Require(t, err)
	retryTxId := redeemReceipt.Logs[0].Topics[2]

	// check the receipt for the retry
	retryReceipt, err := WaitForTx(ctx, builder.L2.Client, retryTxId, time.Second*1)
	Require(t, err)
	if retryReceipt.Status != types.ReceiptStatusSuccessful {
		Fatal(t, "retry failed")
	}

	infraBalanceAfterRedeem, err := builder.L2.Client.BalanceAt(ctx, infraFeeAddr, nil)
	Require(t, err)
	networkBalanceAfterRedeem, err := builder.L2.Client.BalanceAt(ctx, networkFeeAddr, nil)
	Require(t, err)

	// verify that the increment happened, so we know the retry succeeded
	counter, err := simple.Counter(&bind.CallOpts{})
	Require(t, err)

	if counter != 1 {
		Fatal(t, "Unexpected counter:", counter)
	}

	if len(retryReceipt.Logs) != 1 {
		Fatal(t, "Unexpected log count:", len(retryReceipt.Logs))
	}
	parsed, err := simple.ParseRedeemedEvent(*retryReceipt.Logs[0])
	Require(t, err)
	aliasedSender := util.RemapL1Address(usertxoptsL1.From)
	if parsed.Caller != aliasedSender {
		Fatal(t, "Unexpected caller", parsed.Caller, "expected", aliasedSender)
	}
	if parsed.Redeemer != usertxoptsL2.From {
		Fatal(t, "Unexpected redeemer", parsed.Redeemer, "expected", usertxoptsL2.From)
	}

	infraSubmissionFee := METAmath.BigSub(infraBalanceAfterSubmission, infraBalanceBefore)
	networkSubmissionFee := METAmath.BigSub(networkBalanceAfterSubmission, networkBalanceBefore)
	infraRedeemFee := METAmath.BigSub(infraBalanceAfterRedeem, infraBalanceAfterSubmission)
	networkRedeemFee := METAmath.BigSub(networkBalanceAfterRedeem, networkBalanceAfterSubmission)

	METAGasInfo, err := precompilesgen.NewMETAGasInfo(common.HexToAddress("0x6c"), builder.L2.Client)
	Require(t, err)
	minimumBaseFee, err := METAGasInfo.GetMinimumGasPrice(&bind.CallOpts{Context: ctx})
	Require(t, err)
	submissionBaseFee := builder.L2.GetBaseFeeAt(t, submissionReceipt.BlockNumber)
	submissionTx, ok := submissionTxOuter.GetInner().(*types.metachainSubmitRetryableTx)
	if !ok {
		Fatal(t, "inner tx isn't metachainSubmitRetryableTx")
	}
	// submission + auto redeemed retry expected fees
	retryableSubmissionFee := retryables.RetryableSubmissionFee(len(submissionTx.RetryData), submissionTx.L1BaseFee)
	expectedSubmissionFee := METAmath.BigMulByUint(submissionBaseFee, autoRedeemReceipt.GasUsed)
	expectedInfraSubmissionFee := METAmath.BigMulByUint(minimumBaseFee, autoRedeemReceipt.GasUsed)
	expectedNetworkSubmissionFee := METAmath.BigAdd(
		METAmath.BigSub(expectedSubmissionFee, expectedInfraSubmissionFee),
		retryableSubmissionFee,
	)

	retryTxOuter, _, err := builder.L2.Client.TransactionByHash(ctx, retryTxId)
	Require(t, err)
	retryTx, ok := retryTxOuter.GetInner().(*types.metachainRetryTx)
	if !ok {
		Fatal(t, "inner tx isn't metachainRetryTx")
	}
	redeemBaseFee := builder.L2.GetBaseFeeAt(t, redeemReceipt.BlockNumber)

	t.Log("redeem base fee:", redeemBaseFee)
	// redeem & retry expected fees
	redeemGasUsed := redeemReceipt.GasUsed - redeemReceipt.GasUsedForL1 - retryTx.Gas + retryReceipt.GasUsed
	expectedRedeemFee := METAmath.BigMulByUint(redeemBaseFee, redeemGasUsed)
	expectedInfraRedeemFee := METAmath.BigMulByUint(minimumBaseFee, redeemGasUsed)
	expectedNetworkRedeemFee := METAmath.BigSub(expectedRedeemFee, expectedInfraRedeemFee)

	t.Log("submission gas:         ", submissionReceipt.GasUsed)
	t.Log("auto redeemed retry gas:", autoRedeemReceipt.GasUsed)
	t.Log("redeem gas:             ", redeemReceipt.GasUsed)
	t.Log("retry gas:              ", retryReceipt.GasUsed)
	colors.PrintMint("submission and auto redeemed retry - paid infra fee:        ", infraSubmissionFee)
	colors.PrintBlue("submission and auto redeemed retry - expected infra fee:    ", expectedInfraSubmissionFee)
	colors.PrintMint("submission and auto redeemed retry - paid network fee:      ", networkSubmissionFee)
	colors.PrintBlue("submission and auto redeemed retry - expected network fee:  ", expectedNetworkSubmissionFee)
	colors.PrintMint("redeem and retry - paid infra fee:            ", infraRedeemFee)
	colors.PrintBlue("redeem and retry - expected infra fee:        ", expectedInfraRedeemFee)
	colors.PrintMint("redeem and retry - paid network fee:          ", networkRedeemFee)
	colors.PrintBlue("redeem and retry - expected network fee:      ", expectedNetworkRedeemFee)
	if !METAmath.BigEquals(infraSubmissionFee, expectedInfraSubmissionFee) {
		Fatal(t, "Unexpected infra fee paid by submission and auto redeem, want:", expectedInfraSubmissionFee, "have:", infraSubmissionFee)
	}
	if !METAmath.BigEquals(networkSubmissionFee, expectedNetworkSubmissionFee) {
		Fatal(t, "Unexpected network fee paid by submission and auto redeem, want:", expectedNetworkSubmissionFee, "have:", networkSubmissionFee)
	}
	if !METAmath.BigEquals(infraRedeemFee, expectedInfraRedeemFee) {
		Fatal(t, "Unexpected infra fee paid by redeem and retry, want:", expectedInfraRedeemFee, "have:", infraRedeemFee)
	}
	if !METAmath.BigEquals(networkRedeemFee, expectedNetworkRedeemFee) {
		Fatal(t, "Unexpected network fee paid by redeem and retry, want:", expectedNetworkRedeemFee, "have:", networkRedeemFee)
	}
}

// elevateL2Basefee by burning gas exceeding speed limit
func elevateL2Basefee(t *testing.T, ctx context.Context, builder *NodeBuilder) {
	baseFeeBefore := builder.L2.GetBaseFee(t)
	colors.PrintBlue("Elevating base fee...")
	METAostestabi, err := precompilesgen.METAosTestMetaData.GetAbi()
	Require(t, err)
	_, err = precompilesgen.NewMETAosTest(common.HexToAddress("0x69"), builder.L2.Client)
	Require(t, err, "failed to deploy METAosTest")

	burnAmount := gethexec.ConfigDefaultTest().RPC.RPCGasCap
	burnTarget := uint64(5 * l2pricing.InitialSpeedLimitPerSecondV6 * l2pricing.InitialBacklogTolerance)
	for i := uint64(0); i < (burnTarget+burnAmount)/burnAmount; i++ {
		burnMETAGas := METAostestabi.Methods["burnMETAGas"]
		data, err := burnMETAGas.Inputs.Pack(METAmath.UintToBig(burnAmount - builder.L2Info.TransferGas))
		Require(t, err)
		input := append([]byte{}, burnMETAGas.ID...)
		input = append(input, data...)
		to := common.HexToAddress("0x69")
		tx := builder.L2Info.PrepareTxTo("Faucet", &to, burnAmount, big.NewInt(0), input)
		Require(t, builder.L2.Client.SendTransaction(ctx, tx))
		_, err = builder.L2.EnsureTxSucceeded(tx)
		Require(t, err)
	}
	baseFee := builder.L2.GetBaseFee(t)
	colors.PrintBlue("New base fee: ", baseFee, " diff:", baseFee.Uint64()-baseFeeBefore.Uint64())
}

func setupFeeAddresses(t *testing.T, ctx context.Context, builder *NodeBuilder) (common.Address, common.Address) {
	ownerTxOpts := builder.L2Info.GetDefaultTransactOpts("Owner", ctx)
	ownerCallOpts := builder.L2Info.GetDefaultCallOpts("Owner", ctx)
	// make "Owner" a chain owner
	METAdebug, err := precompilesgen.NewMETADebug(common.HexToAddress("0xff"), builder.L2.Client)
	Require(t, err, "failed to deploy METADebug")
	tx, err := METAdebug.BecomeChainOwner(&ownerTxOpts)
	Require(t, err, "failed to deploy METADebug")
	_, err = builder.L2.EnsureTxSucceeded(tx)
	Require(t, err)
	METAowner, err := precompilesgen.NewMETAOwner(common.HexToAddress("70"), builder.L2.Client)
	Require(t, err)
	METAownerPublic, err := precompilesgen.NewMETAOwnerPublic(common.HexToAddress("6b"), builder.L2.Client)
	Require(t, err)
	builder.L2Info.GenerateAccount("InfraFee")
	builder.L2Info.GenerateAccount("NetworkFee")
	networkFeeAddr := builder.L2Info.GetAddress("NetworkFee")
	infraFeeAddr := builder.L2Info.GetAddress("InfraFee")
	tx, err = METAowner.SetNetworkFeeAccount(&ownerTxOpts, networkFeeAddr)
	Require(t, err)
	_, err = builder.L2.EnsureTxSucceeded(tx)
	Require(t, err)
	networkFeeAccount, err := METAownerPublic.GetNetworkFeeAccount(ownerCallOpts)
	Require(t, err)
	tx, err = METAowner.SetInfraFeeAccount(&ownerTxOpts, infraFeeAddr)
	Require(t, err)
	_, err = builder.L2.EnsureTxSucceeded(tx)
	Require(t, err)
	infraFeeAccount, err := METAownerPublic.GetInfraFeeAccount(ownerCallOpts)
	Require(t, err)
	t.Log("Infra fee account: ", infraFeeAccount)
	t.Log("Network fee account: ", networkFeeAccount)
	return infraFeeAddr, networkFeeAddr
}
