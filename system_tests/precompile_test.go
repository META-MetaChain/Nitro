// Copyright 2021-2023, Offchain Labs, Inc.
// For license information, see https://github.com/META-MetaChain/nitro/blob/master/LICENSE

package METAtest

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
	"github.com/META-MetaChain/nitro/METAos"
	"github.com/META-MetaChain/nitro/solgen/go/mocksgen"
	"github.com/META-MetaChain/nitro/solgen/go/precompilesgen"
	"github.com/META-MetaChain/nitro/util/METAmath"
)

func TestPurePrecompileMethodCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	builder := NewNodeBuilder(ctx).DefaultConfig(t, false)
	cleanup := builder.Build(t)
	defer cleanup()

	METASys, err := precompilesgen.NewMETASys(common.HexToAddress("0x64"), builder.L2.Client)
	Require(t, err, "could not deploy METASys contract")
	chainId, err := METASys.METAChainID(&bind.CallOpts{})
	Require(t, err, "failed to get the ChainID")
	if chainId.Uint64() != params.metachainDevTestChainConfig().ChainID.Uint64() {
		Fatal(t, "Wrong ChainID", chainId.Uint64())
	}
}

func TestViewLogReverts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	builder := NewNodeBuilder(ctx).DefaultConfig(t, false)
	cleanup := builder.Build(t)
	defer cleanup()

	METADebug, err := precompilesgen.NewMETADebug(common.HexToAddress("0xff"), builder.L2.Client)
	Require(t, err, "could not deploy METASys contract")

	err = METADebug.EventsView(nil)
	if err == nil {
		Fatal(t, "unexpected success")
	}
}

func TestCustomSolidityErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	builder := NewNodeBuilder(ctx).DefaultConfig(t, false)
	cleanup := builder.Build(t)
	defer cleanup()

	callOpts := &bind.CallOpts{Context: ctx}
	METADebug, err := precompilesgen.NewMETADebug(common.HexToAddress("0xff"), builder.L2.Client)
	Require(t, err, "could not bind METADebug contract")
	customError := METADebug.CustomRevert(callOpts, 1024)
	if customError == nil {
		Fatal(t, "customRevert call should have errored")
	}
	observedMessage := customError.Error()
	expectedMessage := "execution reverted: error Custom(1024, This spider family wards off bugs: /\\oo/\\ //\\(oo)/\\ /\\oo/\\, true)"
	if observedMessage != expectedMessage {
		Fatal(t, observedMessage)
	}

	METASys, err := precompilesgen.NewMETASys(METAos.METASysAddress, builder.L2.Client)
	Require(t, err, "could not bind METASys contract")
	_, customError = METASys.METABlockHash(callOpts, big.NewInt(1e9))
	if customError == nil {
		Fatal(t, "out of range METABlockHash call should have errored")
	}
	observedMessage = customError.Error()
	expectedMessage = "execution reverted: error InvalidBlockNumber(1000000000, 1)"
	if observedMessage != expectedMessage {
		Fatal(t, observedMessage)
	}
}

func TestPrecompileErrorGasLeft(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	builder := NewNodeBuilder(ctx).DefaultConfig(t, false)
	cleanup := builder.Build(t)
	defer cleanup()

	auth := builder.L2Info.GetDefaultTransactOpts("Faucet", ctx)
	_, _, simple, err := mocksgen.DeploySimple(&auth, builder.L2.Client)
	Require(t, err)

	assertNotAllGasConsumed := func(to common.Address, input []byte) {
		gas, err := simple.CheckGasUsed(&bind.CallOpts{Context: ctx}, to, input)
		Require(t, err, "Failed to call CheckGasUsed to precompile", to)
		maxGas := big.NewInt(100_000)
		if METAmath.BigGreaterThan(gas, maxGas) {
			Fatal(t, "Precompile", to, "used", gas, "gas reverting, greater than max expected", maxGas)
		}
	}

	METASys, err := precompilesgen.METASysMetaData.GetAbi()
	Require(t, err)

	METABlockHash := METASys.Methods["METABlockHash"]
	data, err := METABlockHash.Inputs.Pack(big.NewInt(1e9))
	Require(t, err)
	input := append([]byte{}, METABlockHash.ID...)
	input = append(input, data...)
	assertNotAllGasConsumed(METAos.METASysAddress, input)

	METADebug, err := precompilesgen.METADebugMetaData.GetAbi()
	Require(t, err)
	assertNotAllGasConsumed(common.HexToAddress("0xff"), METADebug.Methods["legacyError"].ID)
}
