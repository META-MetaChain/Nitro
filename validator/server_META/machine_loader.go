package server_META

import (
	"context"

	"github.com/ethereum/go-ethereum/common"
	"github.com/META-MetaChain/nitro/validator/server_common"
)

type METAitratorMachineConfig struct {
	WavmBinaryPath       string
	UntilHostIoStatePath string
}

var DefaultMETAitratorMachineConfig = METAitratorMachineConfig{
	WavmBinaryPath:       "machine.wavm.br",
	UntilHostIoStatePath: "until-host-io-state.bin",
}

type METAMachines struct {
	zeroStep *METAitratorMachine
	hostIo   *METAitratorMachine
}

type METAMachineLoader struct {
	server_common.MachineLoader[METAMachines]
}

func NewMETAMachineLoader(config *METAitratorMachineConfig, locator *server_common.MachineLocator) *METAMachineLoader {
	createMachineFunc := func(ctx context.Context, moduleRoot common.Hash) (*METAMachines, error) {
		return createMETAMachine(ctx, locator, config, moduleRoot)
	}
	return &METAMachineLoader{
		MachineLoader: *server_common.NewMachineLoader[METAMachines](locator, createMachineFunc),
	}
}

func (a *METAMachineLoader) GetHostIoMachine(ctx context.Context, moduleRoot common.Hash) (*METAitratorMachine, error) {
	machines, err := a.GetMachine(ctx, moduleRoot)
	if err != nil {
		return nil, err
	}
	return machines.hostIo, nil
}

func (a *METAMachineLoader) GetZeroStepMachine(ctx context.Context, moduleRoot common.Hash) (*METAitratorMachine, error) {
	machines, err := a.GetMachine(ctx, moduleRoot)
	if err != nil {
		return nil, err
	}
	return machines.zeroStep, nil
}
