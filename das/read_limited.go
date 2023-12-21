// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

package das

import (
	"context"
	"fmt"

	"github.com/META-MetaChain/nitro/METAstate"
)

// These classes are wrappers implementing das.StorageService and das.DataAvailabilityService.
// They are needed to make the DAS factory function uniform for all allowed configurations.
// The wrappers panic if they are used in a situation where writes are needed; panic is used because
// it is a programming error in the code setting up the node or daserver if a non-writeable object
// is used in a writeable context.

func NewReadLimitedStorageService(reader METAstate.DataAvailabilityReader) *readLimitedStorageService {
	return &readLimitedStorageService{reader}
}

type readLimitedStorageService struct {
	METAstate.DataAvailabilityReader
}

func (s *readLimitedStorageService) Put(ctx context.Context, data []byte, expiration uint64) error {
	panic("Logic error: readLimitedStorageService.Put shouldn't be called.")
}

func (s *readLimitedStorageService) Sync(ctx context.Context) error {
	panic("Logic error: readLimitedStorageService.Store shouldn't be called.")
}

func (s *readLimitedStorageService) Close(ctx context.Context) error {
	return nil
}

func (s *readLimitedStorageService) String() string {
	return fmt.Sprintf("readLimitedStorageService(%v)", s.DataAvailabilityReader)

}

type readLimitedDataAvailabilityService struct {
	METAstate.DataAvailabilityReader
}

func NewReadLimitedDataAvailabilityService(da METAstate.DataAvailabilityReader) *readLimitedDataAvailabilityService {
	return &readLimitedDataAvailabilityService{da}
}

func (*readLimitedDataAvailabilityService) Store(ctx context.Context, message []byte, timeout uint64, sig []byte) (*METAstate.DataAvailabilityCertificate, error) {
	panic("Logic error: readLimitedDataAvailabilityService.Store shouldn't be called.")
}

func (s *readLimitedDataAvailabilityService) String() string {
	return fmt.Sprintf("ReadLimitedDataAvailabilityService(%v)", s.DataAvailabilityReader)
}
