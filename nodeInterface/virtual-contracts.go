// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

package nodeInterface

import (
	"context"
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/metachain"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/log"
	"github.com/META-MetaChain/nitro/METAnode"
	"github.com/META-MetaChain/nitro/METAos"
	"github.com/META-MetaChain/nitro/METAos/METAosState"
	"github.com/META-MetaChain/nitro/METAos/l1pricing"
	"github.com/META-MetaChain/nitro/gethhook"
	"github.com/META-MetaChain/nitro/precompiles"
	"github.com/META-MetaChain/nitro/solgen/go/node_interfacegen"
	"github.com/META-MetaChain/nitro/solgen/go/precompilesgen"
	"github.com/META-MetaChain/nitro/util/METAmath"
)

type addr = common.Address
type mech = *vm.EVM
type huge = *big.Int
type hash = common.Hash
type bytes32 = [32]byte
type ctx = *precompiles.Context

type BackendAPI = core.NodeInterfaceBackendAPI
type ExecutionResult = core.ExecutionResult

func init() {
	gethhook.RequireHookedGeth()

	nodeInterfaceImpl := &NodeInterface{Address: types.NodeInterfaceAddress}
	nodeInterfaceMeta := node_interfacegen.NodeInterfaceMetaData
	_, nodeInterface := precompiles.MakePrecompile(nodeInterfaceMeta, nodeInterfaceImpl)

	nodeInterfaceDebugImpl := &NodeInterfaceDebug{Address: types.NodeInterfaceDebugAddress}
	nodeInterfaceDebugMeta := node_interfacegen.NodeInterfaceDebugMetaData
	_, nodeInterfaceDebug := precompiles.MakePrecompile(nodeInterfaceDebugMeta, nodeInterfaceDebugImpl)

	core.InterceptRPCMessage = func(
		msg *core.Message,
		ctx context.Context,
		statedb *state.StateDB,
		header *types.Header,
		backend core.NodeInterfaceBackendAPI,
		blockCtx *vm.BlockContext,
	) (*core.Message, *ExecutionResult, error) {
		to := msg.To
		METAosVersion := METAosState.METAOSVersion(statedb) // check METAOS has been installed
		if to != nil && METAosVersion != 0 {
			var precompile precompiles.METAosPrecompile
			var swapMessages bool
			returnMessage := &core.Message{}
			var address addr

			switch *to {
			case types.NodeInterfaceAddress:
				address = types.NodeInterfaceAddress
				duplicate := *nodeInterfaceImpl
				duplicate.backend = backend
				duplicate.context = ctx
				duplicate.header = header
				duplicate.sourceMessage = msg
				duplicate.returnMessage.message = returnMessage
				duplicate.returnMessage.changed = &swapMessages
				precompile = nodeInterface.CloneWithImpl(&duplicate)
			case types.NodeInterfaceDebugAddress:
				address = types.NodeInterfaceDebugAddress
				duplicate := *nodeInterfaceDebugImpl
				duplicate.backend = backend
				duplicate.context = ctx
				duplicate.header = header
				duplicate.sourceMessage = msg
				duplicate.returnMessage.message = returnMessage
				duplicate.returnMessage.changed = &swapMessages
				precompile = nodeInterfaceDebug.CloneWithImpl(&duplicate)
			default:
				return msg, nil, nil
			}

			evm, vmError := backend.GetEVM(ctx, msg, statedb, header, &vm.Config{NoBaseFee: true}, blockCtx)
			go func() {
				<-ctx.Done()
				evm.Cancel()
			}()
			core.ReadyEVMForL2(evm, msg)

			output, gasLeft, err := precompile.Call(
				msg.Data, address, address, msg.From, msg.Value, false, msg.GasLimit, evm,
			)
			if err != nil {
				return msg, nil, err
			}
			if swapMessages {
				return returnMessage, nil, nil
			}
			res := &ExecutionResult{
				UsedGas:       msg.GasLimit - gasLeft,
				Err:           nil,
				ReturnData:    output,
				ScheduledTxes: nil,
			}
			return msg, res, vmError()
		}
		return msg, nil, nil
	}

	core.InterceptRPCGasCap = func(gascap *uint64, msg *core.Message, header *types.Header, statedb *state.StateDB) {
		if *gascap == 0 {
			// It's already unlimited
			return
		}
		METAosVersion := METAosState.METAOSVersion(statedb)
		if METAosVersion == 0 {
			// METAOS hasn't been installed, so use the vanilla gas cap
			return
		}
		state, err := METAosState.OpenSystemMETAosState(statedb, nil, true)
		if err != nil {
			log.Error("failed to open METAOS state", "err", err)
			return
		}
		if header.BaseFee.Sign() == 0 {
			// if gas is free or there's no reimbursable poster, the user won't pay for L1 data costs
			return
		}

		brotliCompressionLevel, err := state.BrotliCompressionLevel()
		if err != nil {
			log.Error("failed to get brotli compression level", "err", err)
			return
		}
		posterCost, _ := state.L1PricingState().PosterDataCost(msg, l1pricing.BatchPosterAddress, brotliCompressionLevel)
		posterCostInL2Gas := METAos.GetPosterGas(state, header.BaseFee, msg.TxRunMode, posterCost)
		*gascap = METAmath.SaturatingUAdd(*gascap, posterCostInL2Gas)
	}

	core.GetMETAOSSpeedLimitPerSecond = func(statedb *state.StateDB) (uint64, error) {
		METAosVersion := METAosState.METAOSVersion(statedb)
		if METAosVersion == 0 {
			return 0.0, errors.New("METAOS not installed")
		}
		state, err := METAosState.OpenSystemMETAosState(statedb, nil, true)
		if err != nil {
			log.Error("failed to open METAOS state", "err", err)
			return 0.0, err
		}
		pricing := state.L2PricingState()
		speedLimit, err := pricing.SpeedLimitPerSecond()
		if err != nil {
			log.Error("failed to get the speed limit", "err", err)
			return 0.0, err
		}
		return speedLimit, nil
	}

	METASys, err := precompilesgen.METASysMetaData.GetAbi()
	if err != nil {
		panic(err)
	}
	l2ToL1TxTopic = METASys.Events["L2ToL1Tx"].ID
	l2ToL1TransactionTopic = METASys.Events["L2ToL1Transaction"].ID
	merkleTopic = METASys.Events["SendMerkleUpdate"].ID
}

func METANodeFromNodeInterfaceBackend(backend BackendAPI) (*METAnode.Node, error) {
	apiBackend, ok := backend.(*metachain.APIBackend)
	if !ok {
		return nil, errors.New("API backend isn't metachain")
	}
	METANode, ok := apiBackend.GetmetachainNode().(*METAnode.Node)
	if !ok {
		return nil, errors.New("failed to get metachain Node from backend")
	}
	return METANode, nil
}

func blockchainFromNodeInterfaceBackend(backend BackendAPI) (*core.BlockChain, error) {
	apiBackend, ok := backend.(*metachain.APIBackend)
	if !ok {
		return nil, errors.New("API backend isn't metachain")
	}
	bc := apiBackend.BlockChain()
	if bc == nil {
		return nil, errors.New("failed to get Blockchain from backend")
	}
	return bc, nil
}
