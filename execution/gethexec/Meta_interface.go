// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

package gethexec

import (
	"context"

	"github.com/ethereum/go-ethereum/metachain_types"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
)

type TransactionPublisher interface {
	PublishTransaction(ctx context.Context, tx *types.Transaction, options *metachain_types.ConditionalOptions) error
	CheckHealth(ctx context.Context) error
	Initialize(context.Context) error
	Start(context.Context) error
	StopAndWait()
	Started() bool
}

type METAInterface struct {
	exec        *ExecutionEngine
	txPublisher TransactionPublisher
	METANode     interface{}
}

func NewMETAInterface(exec *ExecutionEngine, txPublisher TransactionPublisher) (*METAInterface, error) {
	return &METAInterface{
		exec:        exec,
		txPublisher: txPublisher,
	}, nil
}

func (a *METAInterface) Initialize(METAnode interface{}) {
	a.METANode = METAnode
}

func (a *METAInterface) PublishTransaction(ctx context.Context, tx *types.Transaction, options *metachain_types.ConditionalOptions) error {
	return a.txPublisher.PublishTransaction(ctx, tx, options)
}

func (a *METAInterface) BlockChain() *core.BlockChain {
	return a.exec.bc
}

func (a *METAInterface) METANode() interface{} {
	return a.METANode
}
