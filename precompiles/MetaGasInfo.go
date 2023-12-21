// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

package precompiles

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
	"github.com/META-MetaChain/nitro/METAos/l1pricing"
	"github.com/META-MetaChain/nitro/METAos/storage"
	"github.com/META-MetaChain/nitro/util/METAmath"
)

// METAGasInfo provides insight into the cost of using the rollup.
type METAGasInfo struct {
	Address addr // 0x6c
}

var storageMETAGas = big.NewInt(int64(storage.StorageWriteCost))

const AssumedSimpleTxSize = 140

// GetPricesInWeiWithAggregator gets  prices in wei when using the provided aggregator
func (con METAGasInfo) GetPricesInWeiWithAggregator(
	c ctx,
	evm mech,
	aggregator addr,
) (huge, huge, huge, huge, huge, huge, error) {
	if c.State.METAOSVersion() < 4 {
		return con._preVersion4_GetPricesInWeiWithAggregator(c, evm, aggregator)
	}

	l1GasPrice, err := c.State.L1PricingState().PricePerUnit()
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	l2GasPrice := evm.Context.BaseFee

	// aggregators compress calldata, so we must estimate accordingly
	weiForL1Calldata := METAmath.BigMulByUint(l1GasPrice, params.TxDataNonZeroGasEIP2028)

	// the cost of a simple tx without calldata
	perL2Tx := METAmath.BigMulByUint(weiForL1Calldata, AssumedSimpleTxSize)

	// nitro's compute-centric l2 gas pricing has no special compute component that rises independently
	perMETAGasBase, err := c.State.L2PricingState().MinBaseFeeWei()
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	if METAmath.BigLessThan(l2GasPrice, perMETAGasBase) {
		perMETAGasBase = l2GasPrice
	}
	perMETAGasCongestion := METAmath.BigSub(l2GasPrice, perMETAGasBase)
	perMETAGasTotal := l2GasPrice

	weiForL2Storage := METAmath.BigMul(l2GasPrice, storageMETAGas)

	return perL2Tx, weiForL1Calldata, weiForL2Storage, perMETAGasBase, perMETAGasCongestion, perMETAGasTotal, nil
}

func (con METAGasInfo) _preVersion4_GetPricesInWeiWithAggregator(
	c ctx,
	evm mech,
	aggregator addr,
) (huge, huge, huge, huge, huge, huge, error) {
	l1GasPrice, err := c.State.L1PricingState().PricePerUnit()
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	l2GasPrice := evm.Context.BaseFee

	// aggregators compress calldata, so we must estimate accordingly
	weiForL1Calldata := METAmath.BigMulByUint(l1GasPrice, params.TxDataNonZeroGasEIP2028)

	// the cost of a simple tx without calldata
	perL2Tx := METAmath.BigMulByUint(weiForL1Calldata, AssumedSimpleTxSize)

	// nitro's compute-centric l2 gas pricing has no special compute component that rises independently
	perMETAGasBase := l2GasPrice
	perMETAGasCongestion := common.Big0
	perMETAGasTotal := l2GasPrice

	weiForL2Storage := METAmath.BigMul(l2GasPrice, storageMETAGas)

	return perL2Tx, weiForL1Calldata, weiForL2Storage, perMETAGasBase, perMETAGasCongestion, perMETAGasTotal, nil
}

// GetPricesInWei gets prices in wei when using the caller's preferred aggregator
func (con METAGasInfo) GetPricesInWei(c ctx, evm mech) (huge, huge, huge, huge, huge, huge, error) {
	return con.GetPricesInWeiWithAggregator(c, evm, addr{})
}

// GetPricesInMETAGasWithAggregator gets prices in METAGas when using the provided aggregator
func (con METAGasInfo) GetPricesInMETAGasWithAggregator(c ctx, evm mech, aggregator addr) (huge, huge, huge, error) {
	if c.State.METAOSVersion() < 4 {
		return con._preVersion4_GetPricesInMETAGasWithAggregator(c, evm, aggregator)
	}
	l1GasPrice, err := c.State.L1PricingState().PricePerUnit()
	if err != nil {
		return nil, nil, nil, err
	}
	l2GasPrice := evm.Context.BaseFee

	// aggregators compress calldata, so we must estimate accordingly
	weiForL1Calldata := METAmath.BigMulByUint(l1GasPrice, params.TxDataNonZeroGasEIP2028)
	weiPerL2Tx := METAmath.BigMulByUint(weiForL1Calldata, AssumedSimpleTxSize)
	gasForL1Calldata := common.Big0
	gasPerL2Tx := common.Big0
	if l2GasPrice.Sign() > 0 {
		gasForL1Calldata = METAmath.BigDiv(weiForL1Calldata, l2GasPrice)
		gasPerL2Tx = METAmath.BigDiv(weiPerL2Tx, l2GasPrice)
	}

	return gasPerL2Tx, gasForL1Calldata, storageMETAGas, nil
}

func (con METAGasInfo) _preVersion4_GetPricesInMETAGasWithAggregator(c ctx, evm mech, aggregator addr) (huge, huge, huge, error) {
	l1GasPrice, err := c.State.L1PricingState().PricePerUnit()
	if err != nil {
		return nil, nil, nil, err
	}
	l2GasPrice := evm.Context.BaseFee

	// aggregators compress calldata, so we must estimate accordingly
	weiForL1Calldata := METAmath.BigMulByUint(l1GasPrice, params.TxDataNonZeroGasEIP2028)
	gasForL1Calldata := common.Big0
	if l2GasPrice.Sign() > 0 {
		gasForL1Calldata = METAmath.BigDiv(weiForL1Calldata, l2GasPrice)
	}

	perL2Tx := big.NewInt(AssumedSimpleTxSize)
	return perL2Tx, gasForL1Calldata, storageMETAGas, nil
}

// GetPricesInMETAGas gets prices in METAGas when using the caller's preferred aggregator
func (con METAGasInfo) GetPricesInMETAGas(c ctx, evm mech) (huge, huge, huge, error) {
	return con.GetPricesInMETAGasWithAggregator(c, evm, addr{})
}

// GetGasAccountingParams gets the rollup's speed limit, pool size, and tx gas limit
func (con METAGasInfo) GetGasAccountingParams(c ctx, evm mech) (huge, huge, huge, error) {
	l2pricing := c.State.L2PricingState()
	speedLimit, _ := l2pricing.SpeedLimitPerSecond()
	maxTxGasLimit, err := l2pricing.PerBlockGasLimit()
	return METAmath.UintToBig(speedLimit), METAmath.UintToBig(maxTxGasLimit), METAmath.UintToBig(maxTxGasLimit), err
}

// GetMinimumGasPrice gets the minimum gas price needed for a transaction to succeed
func (con METAGasInfo) GetMinimumGasPrice(c ctx, evm mech) (huge, error) {
	return c.State.L2PricingState().MinBaseFeeWei()
}

// GetL1BaseFeeEstimate gets the current estimate of the L1 basefee
func (con METAGasInfo) GetL1BaseFeeEstimate(c ctx, evm mech) (huge, error) {
	return c.State.L1PricingState().PricePerUnit()
}

// GetL1BaseFeeEstimateInertia gets how slowly METAOS updates its estimate of the L1 basefee
func (con METAGasInfo) GetL1BaseFeeEstimateInertia(c ctx, evm mech) (uint64, error) {
	return c.State.L1PricingState().Inertia()
}

// GetL1RewardRate gets the L1 pricer reward rate
func (con METAGasInfo) GetL1RewardRate(c ctx, evm mech) (uint64, error) {
	return c.State.L1PricingState().GetRewardsRate()
}

// GetL1RewardRecipient gets the L1 pricer reward recipient
func (con METAGasInfo) GetL1RewardRecipient(c ctx, evm mech) (common.Address, error) {
	return c.State.L1PricingState().GetRewardsRecepient()
}

// GetL1GasPriceEstimate gets the current estimate of the L1 basefee
func (con METAGasInfo) GetL1GasPriceEstimate(c ctx, evm mech) (huge, error) {
	return con.GetL1BaseFeeEstimate(c, evm)
}

// GetCurrentTxL1GasFees gets the fee paid to the aggregator for posting this tx
func (con METAGasInfo) GetCurrentTxL1GasFees(c ctx, evm mech) (huge, error) {
	return c.txProcessor.PosterFee, nil
}

// GetGasBacklog gets the backlogged amount of gas burnt in excess of the speed limit
func (con METAGasInfo) GetGasBacklog(c ctx, evm mech) (uint64, error) {
	return c.State.L2PricingState().GasBacklog()
}

// GetPricingInertia gets the L2 basefee in response to backlogged gas
func (con METAGasInfo) GetPricingInertia(c ctx, evm mech) (uint64, error) {
	return c.State.L2PricingState().PricingInertia()
}

// GetGasBacklogTolerance gets the forgivable amount of backlogged gas METAOS will ignore when raising the basefee
func (con METAGasInfo) GetGasBacklogTolerance(c ctx, evm mech) (uint64, error) {
	return c.State.L2PricingState().BacklogTolerance()
}

func (con METAGasInfo) GetL1PricingSurplus(c ctx, evm mech) (*big.Int, error) {
	if c.State.METAOSVersion() < 10 {
		return con._preversion10_GetL1PricingSurplus(c, evm)
	}
	ps := c.State.L1PricingState()
	fundsDueForRefunds, err := ps.BatchPosterTable().TotalFundsDue()
	if err != nil {
		return nil, err
	}
	fundsDueForRewards, err := ps.FundsDueForRewards()
	if err != nil {
		return nil, err
	}
	haveFunds, err := ps.L1FeesAvailable()
	if err != nil {
		return nil, err
	}
	needFunds := METAmath.BigAdd(fundsDueForRefunds, fundsDueForRewards)
	return METAmath.BigSub(haveFunds, needFunds), nil
}

func (con METAGasInfo) _preversion10_GetL1PricingSurplus(c ctx, evm mech) (*big.Int, error) {
	ps := c.State.L1PricingState()
	fundsDueForRefunds, err := ps.BatchPosterTable().TotalFundsDue()
	if err != nil {
		return nil, err
	}
	fundsDueForRewards, err := ps.FundsDueForRewards()
	if err != nil {
		return nil, err
	}
	haveFunds := evm.StateDB.GetBalance(l1pricing.L1PricerFundsPoolAddress)
	needFunds := METAmath.BigAdd(fundsDueForRefunds, fundsDueForRewards)
	return METAmath.BigSub(haveFunds, needFunds), nil
}

func (con METAGasInfo) GetPerBatchGasCharge(c ctx, evm mech) (int64, error) {
	return c.State.L1PricingState().PerBatchGasCost()
}

func (con METAGasInfo) GetAmortizedCostCapBips(c ctx, evm mech) (uint64, error) {
	return c.State.L1PricingState().AmortizedCostCapBips()
}

func (con METAGasInfo) GetL1FeesAvailable(c ctx, evm mech) (huge, error) {
	return c.State.L1PricingState().L1FeesAvailable()
}
