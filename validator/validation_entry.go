package validator

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/META-MetaChain/nitro/METAutil"
)

type BatchInfo struct {
	Number uint64
	Data   []byte
}

type ValidationInput struct {
	Id            uint64
	HasDelayedMsg bool
	DelayedMsgNr  uint64
	Preimages     map[METAutil.PreimageType]map[common.Hash][]byte
	BatchInfo     []BatchInfo
	DelayedMsg    []byte
	StartState    GoGlobalState
}
