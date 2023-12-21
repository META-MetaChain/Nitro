// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

package precompiles

import (
	"github.com/ethereum/go-ethereum/common"
)

// METAOwnerPublic precompile provides non-owners with info about the current chain owners.
// The calls to this precompile do not require the sender be a chain owner.
// For those that are, see METAOwner
type METAOwnerPublic struct {
	Address                    addr // 0x6b
	ChainOwnerRectified        func(ctx, mech, addr) error
	ChainOwnerRectifiedGasCost func(addr) (uint64, error)
}

// GetAllChainOwners retrieves the list of chain owners
func (con METAOwnerPublic) GetAllChainOwners(c ctx, evm mech) ([]common.Address, error) {
	return c.State.ChainOwners().AllMembers(65536)
}

// RectifyChainOwner checks if the account is a chain owner
func (con METAOwnerPublic) RectifyChainOwner(c ctx, evm mech, addr addr) error {
	err := c.State.ChainOwners().RectifyMapping(addr)
	if err != nil {
		return err
	}
	return con.ChainOwnerRectified(c, evm, addr)
}

// IsChainOwner checks if the user is a chain owner
func (con METAOwnerPublic) IsChainOwner(c ctx, evm mech, addr addr) (bool, error) {
	return c.State.ChainOwners().IsMember(addr)
}

// GetNetworkFeeAccount gets the network fee collector
func (con METAOwnerPublic) GetNetworkFeeAccount(c ctx, evm mech) (addr, error) {
	return c.State.NetworkFeeAccount()
}

// GetInfraFeeAccount gets the infrastructure fee collector
func (con METAOwnerPublic) GetInfraFeeAccount(c ctx, evm mech) (addr, error) {
	if c.State.METAOSVersion() < 6 {
		return c.State.NetworkFeeAccount()
	}
	return c.State.InfraFeeAccount()
}

// GetBrotliCompressionLevel gets the current brotli compression level used for fast compression
func (con METAOwnerPublic) GetBrotliCompressionLevel(c ctx, evm mech) (uint64, error) {
	return c.State.BrotliCompressionLevel()
}
