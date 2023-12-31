// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

// race detection makes things slow and miss timeouts
//go:build challengetest
// +build challengetest

package METAtest

import "testing"

func TestChallengeStakersFaultyHonestActive(t *testing.T) {
	stakerTestImpl(t, true, false)
}

func TestChallengeStakersFaultyHonestInactive(t *testing.T) {
	stakerTestImpl(t, true, true)
}
