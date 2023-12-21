// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

// these tests seems to consume too much memory with race detection
//go:build !race
// +build !race

package METAtest

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/META-MetaChain/nitro/METAcompress"
	"github.com/META-MetaChain/nitro/METAos/l1pricing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/META-MetaChain/nitro/solgen/go/precompilesgen"
	"github.com/META-MetaChain/nitro/util/METAmath"
	"github.com/META-MetaChain/nitro/util/colors"
)

func TestSequencerFeePaid(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	builder := NewNodeBuilder(ctx).DefaultConfig(t, true)
	cleanup := builder.Build(t)
	defer cleanup()

	version := builder.L2.ExecNode.METAInterface.BlockChain().Config().metachainChainParams.InitialMETAOSVersion
	callOpts := builder.L2Info.GetDefaultCallOpts("Owner", ctx)

	// get the network fee account
	METAOwnerPublic, err := precompilesgen.NewMETAOwnerPublic(common.HexToAddress("0x6b"), builder.L2.Client)
	Require(t, err, "failed to deploy contract")
	METAGasInfo, err := precompilesgen.NewMETAGasInfo(common.HexToAddress("0x6c"), builder.L2.Client)
	Require(t, err, "failed to deploy contract")
	METADebug, err := precompilesgen.NewMETADebug(common.HexToAddress("0xff"), builder.L2.Client)
	Require(t, err, "failed to deploy contract")
	networkFeeAccount, err := METAOwnerPublic.GetNetworkFeeAccount(callOpts)
	Require(t, err, "could not get the network fee account")

	l1Estimate, err := METAGasInfo.GetL1BaseFeeEstimate(callOpts)
	Require(t, err)

	baseFee := builder.L2.GetBaseFee(t)
	builder.L2Info.GasPrice = baseFee

	testFees := func(tip uint64) (*big.Int, *big.Int) {
		tipCap := METAmath.BigMulByUint(baseFee, tip)
		txOpts := builder.L2Info.GetDefaultTransactOpts("Faucet", ctx)
		txOpts.GasTipCap = tipCap
		gasPrice := METAmath.BigAdd(baseFee, tipCap)

		networkBefore := builder.L2.GetBalance(t, networkFeeAccount)

		tx, err := METADebug.Events(&txOpts, true, [32]byte{})
		Require(t, err)
		receipt, err := builder.L2.EnsureTxSucceeded(tx)
		Require(t, err)

		networkAfter := builder.L2.GetBalance(t, networkFeeAccount)
		l1Charge := METAmath.BigMulByUint(builder.L2Info.GasPrice, receipt.GasUsedForL1)

		// the network should receive
		//     1. compute costs
		//     2. tip on the compute costs
		//     3. tip on the data costs
		networkRevenue := METAmath.BigSub(networkAfter, networkBefore)
		gasUsedForL2 := receipt.GasUsed - receipt.GasUsedForL1
		feePaidForL2 := METAmath.BigMulByUint(gasPrice, gasUsedForL2)
		tipPaidToNet := METAmath.BigMulByUint(tipCap, receipt.GasUsedForL1)
		gotTip := METAmath.BigEquals(networkRevenue, METAmath.BigAdd(feePaidForL2, tipPaidToNet))
		if !gotTip && version == 9 {
			Fatal(t, "network didn't receive expected payment", networkRevenue, feePaidForL2, tipPaidToNet)
		}
		if gotTip && version != 9 {
			Fatal(t, "tips are somehow enabled")
		}

		txSize := compressedTxSize(t, tx)
		l1GasBought := METAmath.BigDiv(l1Charge, l1Estimate).Uint64()
		l1ChargeExpected := METAmath.BigMulByUint(l1Estimate, txSize*params.TxDataNonZeroGasEIP2028)
		// L1 gas can only be charged in terms of L2 gas, so subtract off any rounding error from the expected value
		l1ChargeExpected.Sub(l1ChargeExpected, new(big.Int).Mod(l1ChargeExpected, builder.L2Info.GasPrice))

		colors.PrintBlue("bytes ", l1GasBought/params.TxDataNonZeroGasEIP2028, txSize)

		if !METAmath.BigEquals(l1Charge, l1ChargeExpected) {
			Fatal(t, "the sequencer's future revenue does not match its costs", l1Charge, l1ChargeExpected)
		}
		return networkRevenue, tipPaidToNet
	}

	if version != 9 {
		testFees(3)
		return
	}

	net0, tip0 := testFees(0)
	net2, tip2 := testFees(2)

	if tip0.Sign() != 0 {
		Fatal(t, "nonzero tip")
	}
	if METAmath.BigEquals(METAmath.BigSub(net2, tip2), net0) {
		Fatal(t, "a tip of 2 should yield a total of 3")
	}
}

func testSequencerPriceAdjustsFrom(t *testing.T, initialEstimate uint64) {
	t.Parallel()

	_ = os.Mkdir("test-data", 0766)
	path := filepath.Join("test-data", fmt.Sprintf("testSequencerPriceAdjustsFrom%v.csv", initialEstimate))

	f, err := os.Create(path)
	Require(t, err)
	defer func() { Require(t, f.Close()) }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	builder := NewNodeBuilder(ctx).DefaultConfig(t, true)
	builder.nodeConfig.DelayedSequencer.FinalizeDistance = 1
	cleanup := builder.Build(t)
	defer cleanup()

	// SimulatedBeacon running in OnDemand block production mode
	// produces blocks in the future so we need this to avoid the batch poster
	// not posting because the txs appear to be in the future.
	builder.nodeConfig.BatchPoster.MaxDelay = -time.Hour

	ownerAuth := builder.L2Info.GetDefaultTransactOpts("Owner", ctx)

	// make ownerAuth a chain owner
	METAdebug, err := precompilesgen.NewMETADebug(common.HexToAddress("0xff"), builder.L2.Client)
	Require(t, err)
	tx, err := METAdebug.BecomeChainOwner(&ownerAuth)
	Require(t, err)
	_, err = builder.L2.EnsureTxSucceeded(tx)

	// use ownerAuth to set the L1 price per unit
	Require(t, err)
	METAOwner, err := precompilesgen.NewMETAOwner(common.HexToAddress("0x70"), builder.L2.Client)
	Require(t, err)
	tx, err = METAOwner.SetL1PricePerUnit(&ownerAuth, METAmath.UintToBig(initialEstimate))
	Require(t, err)
	_, err = WaitForTx(ctx, builder.L2.Client, tx.Hash(), time.Second*5)
	Require(t, err)

	METAGasInfo, err := precompilesgen.NewMETAGasInfo(common.HexToAddress("0x6c"), builder.L2.Client)
	Require(t, err)
	lastEstimate, err := METAGasInfo.GetL1BaseFeeEstimate(&bind.CallOpts{Context: ctx})
	Require(t, err)
	lastBatchCount, err := builder.L2.ConsensusNode.InboxTracker.GetBatchCount()
	Require(t, err)
	l1Header, err := builder.L1.Client.HeaderByNumber(ctx, nil)
	Require(t, err)

	rewardRecipientBalanceBefore := builder.L2.GetBalance(t, l1pricing.BatchPosterAddress)
	timesPriceAdjusted := 0

	colors.PrintBlue("Initial values")
	colors.PrintBlue("    L1 base fee ", l1Header.BaseFee)
	colors.PrintBlue("    L1 estimate ", lastEstimate)

	numRetrogradeMoves := 0
	for i := 0; i < 256; i++ {
		tx, receipt := builder.L2.TransferBalance(t, "Owner", "Owner", common.Big1, builder.L2Info)
		header, err := builder.L2.Client.HeaderByHash(ctx, receipt.BlockHash)
		Require(t, err)

		builder.L1.TransferBalance(t, "Faucet", "Faucet", common.Big1, builder.L1Info) // generate l1 traffic

		units := compressedTxSize(t, tx) * params.TxDataNonZeroGasEIP2028
		estimatedL1FeePerUnit := METAmath.BigDivByUint(METAmath.BigMulByUint(header.BaseFee, receipt.GasUsedForL1), units)

		if !METAmath.BigEquals(lastEstimate, estimatedL1FeePerUnit) {
			l1Header, err = builder.L1.Client.HeaderByNumber(ctx, nil)
			Require(t, err)

			callOpts := &bind.CallOpts{Context: ctx, BlockNumber: receipt.BlockNumber}
			actualL1FeePerUnit, err := METAGasInfo.GetL1BaseFeeEstimate(callOpts)
			Require(t, err)
			surplus, err := METAGasInfo.GetL1PricingSurplus(callOpts)
			Require(t, err)

			colors.PrintGrey("METAOS updated its L1 estimate")
			colors.PrintGrey("    L1 base fee ", l1Header.BaseFee)
			colors.PrintGrey("    L1 estimate ", lastEstimate, " ➤ ", estimatedL1FeePerUnit, " = ", actualL1FeePerUnit)
			colors.PrintGrey("    Surplus ", surplus)
			fmt.Fprintf(
				f, "%v, %v, %v, %v, %v, %v\n", i, l1Header.BaseFee, lastEstimate,
				estimatedL1FeePerUnit, actualL1FeePerUnit, surplus,
			)

			oldDiff := METAmath.BigAbs(METAmath.BigSub(lastEstimate, l1Header.BaseFee))
			newDiff := METAmath.BigAbs(METAmath.BigSub(actualL1FeePerUnit, l1Header.BaseFee))
			cmpDiff := METAmath.BigGreaterThan(newDiff, oldDiff)
			signums := surplus.Sign() == METAmath.BigSub(actualL1FeePerUnit, l1Header.BaseFee).Sign()

			if timesPriceAdjusted > 0 && cmpDiff && signums {
				numRetrogradeMoves++
				if numRetrogradeMoves > 1 {
					colors.PrintRed(timesPriceAdjusted, newDiff, oldDiff, lastEstimate, surplus)
					colors.PrintRed(estimatedL1FeePerUnit, l1Header.BaseFee, actualL1FeePerUnit)
					Fatal(t, "L1 gas price estimate should tend toward the basefee")
				}
			} else {
				numRetrogradeMoves = 0
			}
			diff := METAmath.BigAbs(METAmath.BigSub(actualL1FeePerUnit, estimatedL1FeePerUnit))
			maxDiffToAllow := METAmath.BigDivByUint(actualL1FeePerUnit, 100)
			if METAmath.BigLessThan(maxDiffToAllow, diff) { // verify that estimates is within 1% of actual
				Fatal(t, "New L1 estimate differs too much from receipt")
			}
			if METAmath.BigEquals(actualL1FeePerUnit, common.Big0) {
				Fatal(t, "Estimate is zero", i)
			}
			lastEstimate = actualL1FeePerUnit
			timesPriceAdjusted++
		}

		if i%16 == 0 {
			// see that the inbox advances

			for j := 16; j > 0; j-- {
				newBatchCount, err := builder.L2.ConsensusNode.InboxTracker.GetBatchCount()
				Require(t, err)
				if newBatchCount > lastBatchCount {
					colors.PrintGrey("posted new batch ", newBatchCount)
					lastBatchCount = newBatchCount
					break
				}
				if j == 1 {
					Fatal(t, "batch count didn't update in time")
				}
				time.Sleep(time.Millisecond * 100)
			}
		}
	}

	rewardRecipientBalanceAfter := builder.L2.GetBalance(t, builder.chainConfig.metachainChainParams.InitialChainOwner)
	colors.PrintMint("reward recipient balance ", rewardRecipientBalanceBefore, " ➤ ", rewardRecipientBalanceAfter)
	colors.PrintMint("price changes     ", timesPriceAdjusted)

	if timesPriceAdjusted == 0 {
		Fatal(t, "L1 gas price estimate never adjusted")
	}
	if !METAmath.BigGreaterThan(rewardRecipientBalanceAfter, rewardRecipientBalanceBefore) {
		Fatal(t, "reward recipient didn't get paid")
	}

	METAAggregator, err := precompilesgen.NewMETAAggregator(common.HexToAddress("0x6d"), builder.L2.Client)
	Require(t, err)
	batchPosterAddresses, err := METAAggregator.GetBatchPosters(&bind.CallOpts{Context: ctx})
	Require(t, err)
	numReimbursed := 0
	for _, bpAddr := range batchPosterAddresses {
		if bpAddr != l1pricing.BatchPosterAddress && bpAddr != l1pricing.L1PricerFundsPoolAddress {
			numReimbursed++
			bal, err := builder.L1.Client.BalanceAt(ctx, bpAddr, nil)
			Require(t, err)
			if bal.Sign() == 0 {
				Fatal(t, "Batch poster balance is zero for", bpAddr)
			}
		}
	}
	if numReimbursed != 1 {
		Fatal(t, "Wrong number of batch posters were reimbursed", numReimbursed)
	}
}

func TestSequencerPriceAdjustsFrom1Gwei(t *testing.T) {
	testSequencerPriceAdjustsFrom(t, params.GWei)
}

func TestSequencerPriceAdjustsFrom2Gwei(t *testing.T) {
	testSequencerPriceAdjustsFrom(t, 2*params.GWei)
}

func TestSequencerPriceAdjustsFrom5Gwei(t *testing.T) {
	testSequencerPriceAdjustsFrom(t, 5*params.GWei)
}

func TestSequencerPriceAdjustsFrom10Gwei(t *testing.T) {
	testSequencerPriceAdjustsFrom(t, 10*params.GWei)
}

func TestSequencerPriceAdjustsFrom25Gwei(t *testing.T) {
	testSequencerPriceAdjustsFrom(t, 25*params.GWei)
}

func compressedTxSize(t *testing.T, tx *types.Transaction) uint64 {
	txBin, err := tx.MarshalBinary()
	Require(t, err)
	compressed, err := METAcompress.CompressLevel(txBin, 0)
	Require(t, err)
	return uint64(len(compressed))
}
