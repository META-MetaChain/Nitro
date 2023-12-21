package valnode

import (
	"context"

	"github.com/META-MetaChain/nitro/validator"

	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/rpc"
	flag "github.com/spf13/pflag"

	"github.com/META-MetaChain/nitro/validator/server_api"
	"github.com/META-MetaChain/nitro/validator/server_META"
	"github.com/META-MetaChain/nitro/validator/server_common"
	"github.com/META-MetaChain/nitro/validator/server_jit"
)

type WasmConfig struct {
	RootPath               string   `koanf:"root-path"`
	EnableWasmrootsCheck   bool     `koanf:"enable-wasmroots-check"`
	AllowedWasmModuleRoots []string `koanf:"allowed-wasm-module-roots"`
}

func WasmConfigAddOptions(prefix string, f *flag.FlagSet) {
	f.String(prefix+".root-path", DefaultWasmConfig.RootPath, "path to machine folders, each containing wasm files (machine.wavm.br, replay.wasm)")
	f.Bool(prefix+".enable-wasmroots-check", DefaultWasmConfig.EnableWasmrootsCheck, "enable check for compatibility of on-chain WASM module root with node")
	f.StringSlice(prefix+".allowed-wasm-module-roots", DefaultWasmConfig.AllowedWasmModuleRoots, "list of WASM module roots to check if the on-chain WASM module root belongs to on node startup")
}

var DefaultWasmConfig = WasmConfig{
	RootPath:               "",
	EnableWasmrootsCheck:   true,
	AllowedWasmModuleRoots: []string{},
}

type Config struct {
	UseJit     bool                               `koanf:"use-jit"`
	ApiAuth    bool                               `koanf:"api-auth"`
	ApiPublic  bool                               `koanf:"api-public"`
	METAitrator server_META.METAitratorSpawnerConfig `koanf:"METAitrator" reload:"hot"`
	Jit        server_jit.JitSpawnerConfig        `koanf:"jit" reload:"hot"`
	Wasm       WasmConfig                         `koanf:"wasm"`
}

type ValidationConfigFetcher func() *Config

var DefaultValidationConfig = Config{
	UseJit:     true,
	Jit:        server_jit.DefaultJitSpawnerConfig,
	ApiAuth:    true,
	ApiPublic:  false,
	METAitrator: server_META.DefaultMETAitratorSpawnerConfig,
	Wasm:       DefaultWasmConfig,
}

var TestValidationConfig = Config{
	UseJit:     true,
	Jit:        server_jit.DefaultJitSpawnerConfig,
	ApiAuth:    false,
	ApiPublic:  true,
	METAitrator: server_META.DefaultMETAitratorSpawnerConfig,
	Wasm:       DefaultWasmConfig,
}

func ValidationConfigAddOptions(prefix string, f *flag.FlagSet) {
	f.Bool(prefix+".use-jit", DefaultValidationConfig.UseJit, "use jit for validation")
	f.Bool(prefix+".api-auth", DefaultValidationConfig.ApiAuth, "validate is an authenticated API")
	f.Bool(prefix+".api-public", DefaultValidationConfig.ApiPublic, "validate is a public API")
	server_META.METAitratorSpawnerConfigAddOptions(prefix+".METAitrator", f)
	server_jit.JitSpawnerConfigAddOptions(prefix+".jit", f)
	WasmConfigAddOptions(prefix+".wasm", f)
}

type ValidationNode struct {
	config     ValidationConfigFetcher
	METASpawner *server_META.METAitratorSpawner
	jitSpawner *server_jit.JitSpawner
}

func EnsureValidationExposedViaAuthRPC(stackConf *node.Config) {
	found := false
	for _, module := range stackConf.AuthModules {
		if module == server_api.Namespace {
			found = true
			break
		}
	}
	if !found {
		stackConf.AuthModules = append(stackConf.AuthModules, server_api.Namespace)
	}
}

func CreateValidationNode(configFetcher ValidationConfigFetcher, stack *node.Node, fatalErrChan chan error) (*ValidationNode, error) {
	config := configFetcher()
	locator, err := server_common.NewMachineLocator(config.Wasm.RootPath)
	if err != nil {
		return nil, err
	}
	METAConfigFetcher := func() *server_META.METAitratorSpawnerConfig {
		return &configFetcher().METAitrator
	}
	METASpawner, err := server_META.NewMETAitratorSpawner(locator, METAConfigFetcher)
	if err != nil {
		return nil, err
	}
	var serverAPI *server_api.ExecServerAPI
	var jitSpawner *server_jit.JitSpawner
	if config.UseJit {
		jitConfigFetcher := func() *server_jit.JitSpawnerConfig { return &configFetcher().Jit }
		var err error
		jitSpawner, err = server_jit.NewJitSpawner(locator, jitConfigFetcher, fatalErrChan)
		if err != nil {
			return nil, err
		}
		serverAPI = server_api.NewExecutionServerAPI(jitSpawner, METASpawner, METAConfigFetcher)
	} else {
		serverAPI = server_api.NewExecutionServerAPI(METASpawner, METASpawner, METAConfigFetcher)
	}
	valAPIs := []rpc.API{{
		Namespace:     server_api.Namespace,
		Version:       "1.0",
		Service:       serverAPI,
		Public:        config.ApiPublic,
		Authenticated: config.ApiAuth,
	}}
	stack.RegisterAPIs(valAPIs)

	return &ValidationNode{configFetcher, METASpawner, jitSpawner}, nil
}

func (v *ValidationNode) Start(ctx context.Context) error {
	if err := v.METASpawner.Start(ctx); err != nil {
		return err
	}
	if v.jitSpawner != nil {
		if err := v.jitSpawner.Start(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (v *ValidationNode) GetExec() validator.ExecutionSpawner {
	return v.METASpawner
}
