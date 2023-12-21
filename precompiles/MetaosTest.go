// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

package precompiles

import (
	"errors"
)

// METAosTest provides a method of burning METAitrary amounts of gas, which exists for historical reasons.
type METAosTest struct {
	Address addr // 0x69
}

// BurnMETAGas unproductively burns the amount of L2 METAGas
func (con METAosTest) BurnMETAGas(c ctx, gasAmount huge) error {
	if !gasAmount.IsUint64() {
		return errors.New("not a uint64")
	}
	//nolint:errcheck
	c.Burn(gasAmount.Uint64()) // burn the amount, even if it's more than the user has
	return nil
}
