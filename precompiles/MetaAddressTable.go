// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

package precompiles

import (
	"errors"
	"math/big"
)

// METAAddressTable precompile provides the ability to create short-hands for commonly used accounts.
type METAAddressTable struct {
	Address addr // 0x66
}

// AddressExists checks if an address exists in the table
func (con METAAddressTable) AddressExists(c ctx, evm mech, addr addr) (bool, error) {
	return c.State.AddressTable().AddressExists(addr)
}

// Compress and returns the bytes that represent the address
func (con METAAddressTable) Compress(c ctx, evm mech, addr addr) ([]uint8, error) {
	return c.State.AddressTable().Compress(addr)
}

// Decompress the compressed bytes at the given offset with those of the corresponding account
func (con METAAddressTable) Decompress(c ctx, evm mech, buf []uint8, offset huge) (addr, huge, error) {
	if !offset.IsInt64() {
		return addr{}, nil, errors.New("invalid offset in METAAddressTable.Decompress")
	}
	ioffset := offset.Int64()
	if ioffset > int64(len(buf)) {
		return addr{}, nil, errors.New("invalid offset in METAAddressTable.Decompress")
	}
	result, nbytes, err := c.State.AddressTable().Decompress(buf[ioffset:])
	return result, big.NewInt(int64(nbytes)), err
}

// Lookup the index of an address in the table
func (con METAAddressTable) Lookup(c ctx, evm mech, addr addr) (huge, error) {
	result, exists, err := c.State.AddressTable().Lookup(addr)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.New("address does not exist in AddressTable")
	}
	return big.NewInt(int64(result)), nil
}

// LookupIndex for  an address in the table by index
func (con METAAddressTable) LookupIndex(c ctx, evm mech, index huge) (addr, error) {
	if !index.IsUint64() {
		return addr{}, errors.New("invalid index in METAAddressTable.LookupIndex")
	}
	result, exists, err := c.State.AddressTable().LookupIndex(index.Uint64())
	if err != nil {
		return addr{}, err
	}
	if !exists {
		return addr{}, errors.New("index does not exist in AddressTable")
	}
	return result, nil
}

// Register adds an account to the table, shrinking its compressed representation
func (con METAAddressTable) Register(c ctx, evm mech, addr addr) (huge, error) {
	slot, err := c.State.AddressTable().Register(addr)
	return big.NewInt(int64(slot)), err
}

// Size gets the number of addresses in the table
func (con METAAddressTable) Size(c ctx, evm mech) (huge, error) {
	size, err := c.State.AddressTable().Size()
	return big.NewInt(int64(size)), err
}
