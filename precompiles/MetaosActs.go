//
// Copyright 2022, Offchain Labs, Inc. All rights reserved.
//

package precompiles

// METAosActs precompile represents METAOS's internal actions as calls it makes to itself
type METAosActs struct {
	Address addr // 0xa4b05

	CallerNotMETAOSError func() error
}

func (con METAosActs) StartBlock(c ctx, evm mech, l1BaseFee huge, l1BlockNumber, l2BlockNumber, timeLastBlock uint64) error {
	return con.CallerNotMETAOSError()
}

func (con METAosActs) BatchPostingReport(c ctx, evm mech, batchTimestamp huge, batchPosterAddress addr, batchNumber uint64, batchDataGas uint64, l1BaseFeeWei huge) error {
	return con.CallerNotMETAOSError()
}
