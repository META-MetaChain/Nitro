// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

package precompiles

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"

	"github.com/META-MetaChain/nitro/METAos/l1pricing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
)

// METAOwner precompile provides owners with tools for managing the rollup.
// All calls to this precompile are authorized by the OwnerPrecompile wrapper,
// which ensures only a chain owner can access these methods. For methods that
// are safe for non-owners to call, see METAOwnerOld
type METAOwner struct {
	Address          addr // 0x70
	OwnerActs        func(ctx, mech, bytes4, addr, []byte) error
	OwnerActsGasCost func(bytes4, addr, []byte) (uint64, error)
}

var (
	ErrOutOfBounds = errors.New("value out of bounds")
)

// AddChainOwner adds account as a chain owner
func (con METAOwner) AddChainOwner(c ctx, evm mech, newOwner addr) error {
	return c.State.ChainOwners().Add(newOwner)
}

// RemoveChainOwner removes account from the list of chain owners
func (con METAOwner) RemoveChainOwner(c ctx, evm mech, addr addr) error {
	member, _ := con.IsChainOwner(c, evm, addr)
	if !member {
		return errors.New("tried to remove non-owner")
	}
	return c.State.ChainOwners().Remove(addr, c.State.METAOSVersion())
}

// IsChainOwner checks if the account is a chain owner
func (con METAOwner) IsChainOwner(c ctx, evm mech, addr addr) (bool, error) {
	return c.State.ChainOwners().IsMember(addr)
}

// GetAllChainOwners retrieves the list of chain owners
func (con METAOwner) GetAllChainOwners(c ctx, evm mech) ([]common.Address, error) {
	return c.State.ChainOwners().AllMembers(65536)
}

// SetL1BaseFeeEstimateInertia sets how slowly METAOS updates its estimate of the L1 basefee
func (con METAOwner) SetL1BaseFeeEstimateInertia(c ctx, evm mech, inertia uint64) error {
	return c.State.L1PricingState().SetInertia(inertia)
}

// SetL2BaseFee sets the L2 gas price directly, bypassing the pool calculus
func (con METAOwner) SetL2BaseFee(c ctx, evm mech, priceInWei huge) error {
	return c.State.L2PricingState().SetBaseFeeWei(priceInWei)
}

// SetMinimumL2BaseFee sets the minimum base fee needed for a transaction to succeed
func (con METAOwner) SetMinimumL2BaseFee(c ctx, evm mech, priceInWei huge) error {
	return c.State.L2PricingState().SetMinBaseFeeWei(priceInWei)
}

// SetSpeedLimit sets the computational speed limit for the chain
func (con METAOwner) SetSpeedLimit(c ctx, evm mech, limit uint64) error {
	return c.State.L2PricingState().SetSpeedLimitPerSecond(limit)
}

// SetMaxTxGasLimit sets the maximum size a tx (and block) can be
func (con METAOwner) SetMaxTxGasLimit(c ctx, evm mech, limit uint64) error {
	return c.State.L2PricingState().SetMaxPerBlockGasLimit(limit)
}

// SetL2GasPricingInertia sets the L2 gas pricing inertia
func (con METAOwner) SetL2GasPricingInertia(c ctx, evm mech, sec uint64) error {
	return c.State.L2PricingState().SetPricingInertia(sec)
}

// SetL2GasBacklogTolerance sets the L2 gas backlog tolerance
func (con METAOwner) SetL2GasBacklogTolerance(c ctx, evm mech, sec uint64) error {
	return c.State.L2PricingState().SetBacklogTolerance(sec)
}

// GetNetworkFeeAccount gets the network fee collector
func (con METAOwner) GetNetworkFeeAccount(c ctx, evm mech) (addr, error) {
	return c.State.NetworkFeeAccount()
}

// GetInfraFeeAccount gets the infrastructure fee collector
func (con METAOwner) GetInfraFeeAccount(c ctx, evm mech) (addr, error) {
	return c.State.InfraFeeAccount()
}

// SetNetworkFeeAccount sets the network fee collector to the new network fee account
func (con METAOwner) SetNetworkFeeAccount(c ctx, evm mech, newNetworkFeeAccount addr) error {
	return c.State.SetNetworkFeeAccount(newNetworkFeeAccount)
}

// SetInfraFeeAccount sets the infra fee collector to the new network fee account
func (con METAOwner) SetInfraFeeAccount(c ctx, evm mech, newNetworkFeeAccount addr) error {
	return c.State.SetInfraFeeAccount(newNetworkFeeAccount)
}

// ScheduleMETAOSUpgrade to the requested version at the requested timestamp
func (con METAOwner) ScheduleMETAOSUpgrade(c ctx, evm mech, newVersion uint64, timestamp uint64) error {
	return c.State.ScheduleMETAOSUpgrade(newVersion, timestamp)
}

func (con METAOwner) SetL1PricingEquilibrationUnits(c ctx, evm mech, equilibrationUnits huge) error {
	return c.State.L1PricingState().SetEquilibrationUnits(equilibrationUnits)
}

func (con METAOwner) SetL1PricingInertia(c ctx, evm mech, inertia uint64) error {
	return c.State.L1PricingState().SetInertia(inertia)
}

func (con METAOwner) SetL1PricingRewardRecipient(c ctx, evm mech, recipient addr) error {
	return c.State.L1PricingState().SetPayRewardsTo(recipient)
}

func (con METAOwner) SetL1PricingRewardRate(c ctx, evm mech, weiPerUnit uint64) error {
	return c.State.L1PricingState().SetPerUnitReward(weiPerUnit)
}

func (con METAOwner) SetL1PricePerUnit(c ctx, evm mech, pricePerUnit *big.Int) error {
	return c.State.L1PricingState().SetPricePerUnit(pricePerUnit)
}

func (con METAOwner) SetPerBatchGasCharge(c ctx, evm mech, cost int64) error {
	return c.State.L1PricingState().SetPerBatchGasCost(cost)
}

func (con METAOwner) SetAmortizedCostCapBips(c ctx, evm mech, cap uint64) error {
	return c.State.L1PricingState().SetAmortizedCostCapBips(cap)
}

func (con METAOwner) SetBrotliCompressionLevel(c ctx, evm mech, level uint64) error {
	return c.State.SetBrotliCompressionLevel(level)
}

func (con METAOwner) ReleaseL1PricerSurplusFunds(c ctx, evm mech, maxWeiToRelease huge) (huge, error) {
	balance := evm.StateDB.GetBalance(l1pricing.L1PricerFundsPoolAddress)
	l1p := c.State.L1PricingState()
	recognized, err := l1p.L1FeesAvailable()
	if err != nil {
		return nil, err
	}
	weiToTransfer := new(big.Int).Sub(balance, recognized)
	if weiToTransfer.Sign() < 0 {
		return common.Big0, nil
	}
	if weiToTransfer.Cmp(maxWeiToRelease) > 0 {
		weiToTransfer = maxWeiToRelease
	}
	if _, err := l1p.AddToL1FeesAvailable(weiToTransfer); err != nil {
		return nil, err
	}
	return weiToTransfer, nil
}

func (con METAOwner) SetChainConfig(c ctx, evm mech, serializedChainConfig []byte) error {
	if c == nil {
		return errors.New("nil context")
	}
	if c.txProcessor == nil {
		return errors.New("uninitialized tx processor")
	}
	if c.txProcessor.MsgIsNonMutating() {
		var newConfig params.ChainConfig
		err := json.Unmarshal(serializedChainConfig, &newConfig)
		if err != nil {
			return fmt.Errorf("invalid chain config, can't deserialize: %w", err)
		}
		if newConfig.ChainID == nil {
			return errors.New("invalid chain config, missing chain id")
		}
		chainId, err := c.State.ChainId()
		if err != nil {
			return fmt.Errorf("failed to get chain id from METAOS state: %w", err)
		}
		if newConfig.ChainID.Cmp(chainId) != 0 {
			return fmt.Errorf("invalid chain config, chain id mismatch, want: %v, have: %v", chainId, newConfig.ChainID)
		}
		oldSerializedConfig, err := c.State.ChainConfig()
		if err != nil {
			return fmt.Errorf("failed to get old chain config from METAOS state: %w", err)
		}
		if bytes.Equal(oldSerializedConfig, serializedChainConfig) {
			return errors.New("new chain config is the same as old one in METAOS state")
		}
		if len(oldSerializedConfig) != 0 {
			var oldConfig params.ChainConfig
			err = json.Unmarshal(oldSerializedConfig, &oldConfig)
			if err != nil {
				return fmt.Errorf("failed to deserialize old chain config: %w", err)
			}
			if err := oldConfig.CheckCompatible(&newConfig, evm.Context.BlockNumber.Uint64(), evm.Context.Time); err != nil {
				return fmt.Errorf("invalid chain config, not compatible with previous: %w", err)
			}
		}
		currentConfig := evm.ChainConfig()
		if err := currentConfig.CheckCompatible(&newConfig, evm.Context.BlockNumber.Uint64(), evm.Context.Time); err != nil {
			return fmt.Errorf("invalid chain config, not compatible with EVM's chain config: %w", err)
		}
	}
	return c.State.SetChainConfig(serializedChainConfig)
}
