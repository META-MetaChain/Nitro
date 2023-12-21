package gethexec

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/metachain"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/eth/filters"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/META-MetaChain/nitro/METAos/METAostypes"
	"github.com/META-MetaChain/nitro/METAutil"
	"github.com/META-MetaChain/nitro/execution"
	"github.com/META-MetaChain/nitro/solgen/go/precompilesgen"
	"github.com/META-MetaChain/nitro/util/headerreader"
	flag "github.com/spf13/pflag"
)

type DangerousConfig struct {
	ReorgToBlock int64 `koanf:"reorg-to-block"`
}

var DefaultDangerousConfig = DangerousConfig{
	ReorgToBlock: -1,
}

func DangerousConfigAddOptions(prefix string, f *flag.FlagSet) {
	f.Int64(prefix+".reorg-to-block", DefaultDangerousConfig.ReorgToBlock, "DANGEROUS! forces a reorg to an old block height. To be used for testing only. -1 to disable")
}

type Config struct {
	ParentChainReader         headerreader.Config              `koanf:"parent-chain-reader" reload:"hot"`
	Sequencer                 SequencerConfig                  `koanf:"sequencer" reload:"hot"`
	RecordingDatabase         metachain.RecordingDatabaseConfig `koanf:"recording-database"`
	TxPreChecker              TxPreCheckerConfig               `koanf:"tx-pre-checker" reload:"hot"`
	Forwarder                 ForwarderConfig                  `koanf:"forwarder"`
	ForwardingTarget          string                           `koanf:"forwarding-target"`
	SecondaryForwardingTarget []string                         `koanf:"secondary-forwarding-target"`
	Caching                   CachingConfig                    `koanf:"caching"`
	RPC                       metachain.Config                  `koanf:"rpc"`
	TxLookupLimit             uint64                           `koanf:"tx-lookup-limit"`
	Dangerous                 DangerousConfig                  `koanf:"dangerous"`

	forwardingTarget string
}

func (c *Config) Validate() error {
	if err := c.Sequencer.Validate(); err != nil {
		return err
	}
	if !c.Sequencer.Enable && c.ForwardingTarget == "" {
		return errors.New("ForwardingTarget not set and not sequencer (can use \"null\")")
	}
	if c.ForwardingTarget == "null" {
		c.forwardingTarget = ""
	} else {
		c.forwardingTarget = c.ForwardingTarget
	}
	if c.forwardingTarget != "" && c.Sequencer.Enable {
		return errors.New("ForwardingTarget set and sequencer enabled")
	}
	return nil
}

func ConfigAddOptions(prefix string, f *flag.FlagSet) {
	metachain.ConfigAddOptions(prefix+".rpc", f)
	SequencerConfigAddOptions(prefix+".sequencer", f)
	headerreader.AddOptions(prefix+".parent-chain-reader", f)
	metachain.RecordingDatabaseConfigAddOptions(prefix+".recording-database", f)
	f.String(prefix+".forwarding-target", ConfigDefault.ForwardingTarget, "transaction forwarding target URL, or \"null\" to disable forwarding (iff not sequencer)")
	f.StringSlice(prefix+".secondary-forwarding-target", ConfigDefault.SecondaryForwardingTarget, "secondary transaction forwarding target URL")
	AddOptionsForNodeForwarderConfig(prefix+".forwarder", f)
	TxPreCheckerConfigAddOptions(prefix+".tx-pre-checker", f)
	CachingConfigAddOptions(prefix+".caching", f)
	f.Uint64(prefix+".tx-lookup-limit", ConfigDefault.TxLookupLimit, "retain the ability to lookup transactions by hash for the past N blocks (0 = all blocks)")
	DangerousConfigAddOptions(prefix+".dangerous", f)
}

var ConfigDefault = Config{
	RPC:                       metachain.DefaultConfig,
	Sequencer:                 DefaultSequencerConfig,
	ParentChainReader:         headerreader.DefaultConfig,
	RecordingDatabase:         metachain.DefaultRecordingDatabaseConfig,
	ForwardingTarget:          "",
	SecondaryForwardingTarget: []string{},
	TxPreChecker:              DefaultTxPreCheckerConfig,
	TxLookupLimit:             126_230_400, // 1 year at 4 blocks per second
	Caching:                   DefaultCachingConfig,
	Dangerous:                 DefaultDangerousConfig,
	Forwarder:                 DefaultNodeForwarderConfig,
}

func ConfigDefaultNonSequencerTest() *Config {
	config := ConfigDefault
	config.ParentChainReader = headerreader.TestConfig
	config.Sequencer.Enable = false
	config.Forwarder = DefaultTestForwarderConfig
	config.ForwardingTarget = "null"

	_ = config.Validate()

	return &config
}

func ConfigDefaultTest() *Config {
	config := ConfigDefault
	config.Sequencer = TestSequencerConfig
	config.ForwardingTarget = "null"
	config.ParentChainReader = headerreader.TestConfig

	_ = config.Validate()

	return &config
}

type ConfigFetcher func() *Config

type ExecutionNode struct {
	ChainDB           ethdb.Database
	Backend           *metachain.Backend
	FilterSystem      *filters.FilterSystem
	METAInterface      *METAInterface
	ExecEngine        *ExecutionEngine
	Recorder          *BlockRecorder
	Sequencer         *Sequencer // either nil or same as TxPublisher
	TxPublisher       TransactionPublisher
	ConfigFetcher     ConfigFetcher
	ParentChainReader *headerreader.HeaderReader
	started           atomic.Bool
}

func CreateExecutionNode(
	ctx context.Context,
	stack *node.Node,
	chainDB ethdb.Database,
	l2BlockChain *core.BlockChain,
	l1client METAutil.L1Interface,
	configFetcher ConfigFetcher,
) (*ExecutionNode, error) {
	config := configFetcher()
	execEngine, err := NewExecutionEngine(l2BlockChain)
	if err != nil {
		return nil, err
	}
	recorder := NewBlockRecorder(&config.RecordingDatabase, execEngine, chainDB)
	var txPublisher TransactionPublisher
	var sequencer *Sequencer

	var parentChainReader *headerreader.HeaderReader
	if l1client != nil && !reflect.ValueOf(l1client).IsNil() {
		METASys, _ := precompilesgen.NewMETASys(types.METASysAddress, l1client)
		parentChainReader, err = headerreader.New(ctx, l1client, func() *headerreader.Config { return &configFetcher().ParentChainReader }, METASys)
		if err != nil {
			return nil, err
		}
	}

	if config.Sequencer.Enable {
		seqConfigFetcher := func() *SequencerConfig { return &configFetcher().Sequencer }
		sequencer, err = NewSequencer(execEngine, parentChainReader, seqConfigFetcher)
		if err != nil {
			return nil, err
		}
		txPublisher = sequencer
	} else {
		if config.Forwarder.RedisUrl != "" {
			txPublisher = NewRedisTxForwarder(config.forwardingTarget, &config.Forwarder)
		} else if config.forwardingTarget == "" {
			txPublisher = NewTxDropper()
		} else {
			targets := append([]string{config.forwardingTarget}, config.SecondaryForwardingTarget...)
			txPublisher = NewForwarder(targets, &config.Forwarder)
		}
	}

	txprecheckConfigFetcher := func() *TxPreCheckerConfig { return &configFetcher().TxPreChecker }

	txPublisher = NewTxPreChecker(txPublisher, l2BlockChain, txprecheckConfigFetcher)
	METAInterface, err := NewMETAInterface(execEngine, txPublisher)
	if err != nil {
		return nil, err
	}
	filterConfig := filters.Config{
		LogCacheSize: config.RPC.FilterLogCacheSize,
		Timeout:      config.RPC.FilterTimeout,
	}
	backend, filterSystem, err := metachain.NewBackend(stack, &config.RPC, chainDB, METAInterface, filterConfig)
	if err != nil {
		return nil, err
	}

	apis := []rpc.API{{
		Namespace: "META",
		Version:   "1.0",
		Service:   NewMETAAPI(txPublisher),
		Public:    false,
	}}
	apis = append(apis, rpc.API{
		Namespace: "METAdebug",
		Version:   "1.0",
		Service: NewMETADebugAPI(
			l2BlockChain,
			config.RPC.METADebug.BlockRangeBound,
			config.RPC.METADebug.TimeoutQueueBound,
		),
		Public: false,
	})
	apis = append(apis, rpc.API{
		Namespace: "METAtrace",
		Version:   "1.0",
		Service: NewMETATraceForwarderAPI(
			config.RPC.ClassicRedirect,
			config.RPC.ClassicRedirectTimeout,
		),
		Public: false,
	})
	apis = append(apis, rpc.API{
		Namespace: "debug",
		Service:   eth.NewDebugAPI(eth.NewMETAEthereum(l2BlockChain, chainDB)),
		Public:    false,
	})

	stack.RegisterAPIs(apis)

	return &ExecutionNode{
		ChainDB:           chainDB,
		Backend:           backend,
		FilterSystem:      filterSystem,
		METAInterface:      METAInterface,
		ExecEngine:        execEngine,
		Recorder:          recorder,
		Sequencer:         sequencer,
		TxPublisher:       txPublisher,
		ConfigFetcher:     configFetcher,
		ParentChainReader: parentChainReader,
	}, nil

}

func (n *ExecutionNode) Initialize(ctx context.Context, METAnode interface{}, sync metachain.SyncProgressBackend) error {
	n.METAInterface.Initialize(n)
	err := n.Backend.Start()
	if err != nil {
		return fmt.Errorf("error starting geth backend: %w", err)
	}
	err = n.TxPublisher.Initialize(ctx)
	if err != nil {
		return fmt.Errorf("error initializing transaction publisher: %w", err)
	}
	err = n.Backend.APIBackend().SetSyncBackend(sync)
	if err != nil {
		return fmt.Errorf("error setting sync backend: %w", err)
	}
	return nil
}

// not thread safe
func (n *ExecutionNode) Start(ctx context.Context) error {
	if n.started.Swap(true) {
		return errors.New("already started")
	}
	// TODO after separation
	// err := n.Stack.Start()
	// if err != nil {
	// 	return fmt.Errorf("error starting geth stack: %w", err)
	// }
	n.ExecEngine.Start(ctx)
	err := n.TxPublisher.Start(ctx)
	if err != nil {
		return fmt.Errorf("error starting transaction puiblisher: %w", err)
	}
	if n.ParentChainReader != nil {
		n.ParentChainReader.Start(ctx)
	}
	return nil
}

func (n *ExecutionNode) StopAndWait() {
	if !n.started.Load() {
		return
	}
	// TODO after separation
	// n.Stack.StopRPC() // does nothing if not running
	if n.TxPublisher.Started() {
		n.TxPublisher.StopAndWait()
	}
	n.Recorder.OrderlyShutdown()
	if n.ParentChainReader != nil && n.ParentChainReader.Started() {
		n.ParentChainReader.StopAndWait()
	}
	if n.ExecEngine.Started() {
		n.ExecEngine.StopAndWait()
	}
	n.METAInterface.BlockChain().Stop() // does nothing if not running
	if err := n.Backend.Stop(); err != nil {
		log.Error("backend stop", "err", err)
	}
	// TODO after separation
	// if err := n.Stack.Close(); err != nil {
	// 	log.Error("error on stak close", "err", err)
	// }
}

func (n *ExecutionNode) DigestMessage(num METAutil.MessageIndex, msg *METAostypes.MessageWithMetadata) error {
	return n.ExecEngine.DigestMessage(num, msg)
}
func (n *ExecutionNode) Reorg(count METAutil.MessageIndex, newMessages []METAostypes.MessageWithMetadata, oldMessages []*METAostypes.MessageWithMetadata) error {
	return n.ExecEngine.Reorg(count, newMessages, oldMessages)
}
func (n *ExecutionNode) HeadMessageNumber() (METAutil.MessageIndex, error) {
	return n.ExecEngine.HeadMessageNumber()
}
func (n *ExecutionNode) HeadMessageNumberSync(t *testing.T) (METAutil.MessageIndex, error) {
	return n.ExecEngine.HeadMessageNumberSync(t)
}
func (n *ExecutionNode) NextDelayedMessageNumber() (uint64, error) {
	return n.ExecEngine.NextDelayedMessageNumber()
}
func (n *ExecutionNode) SequenceDelayedMessage(message *METAostypes.L1IncomingMessage, delayedSeqNum uint64) error {
	return n.ExecEngine.SequenceDelayedMessage(message, delayedSeqNum)
}
func (n *ExecutionNode) ResultAtPos(pos METAutil.MessageIndex) (*execution.MessageResult, error) {
	return n.ExecEngine.ResultAtPos(pos)
}

func (n *ExecutionNode) RecordBlockCreation(
	ctx context.Context,
	pos METAutil.MessageIndex,
	msg *METAostypes.MessageWithMetadata,
) (*execution.RecordResult, error) {
	return n.Recorder.RecordBlockCreation(ctx, pos, msg)
}
func (n *ExecutionNode) MarkValid(pos METAutil.MessageIndex, resultHash common.Hash) {
	n.Recorder.MarkValid(pos, resultHash)
}
func (n *ExecutionNode) PrepareForRecord(ctx context.Context, start, end METAutil.MessageIndex) error {
	return n.Recorder.PrepareForRecord(ctx, start, end)
}

func (n *ExecutionNode) Pause() {
	if n.Sequencer != nil {
		n.Sequencer.Pause()
	}
}
func (n *ExecutionNode) Activate() {
	if n.Sequencer != nil {
		n.Sequencer.Activate()
	}
}
func (n *ExecutionNode) ForwardTo(url string) error {
	if n.Sequencer != nil {
		return n.Sequencer.ForwardTo(url)
	} else {
		return errors.New("forwardTo not supported - sequencer not active")
	}
}
func (n *ExecutionNode) SetTransactionStreamer(streamer execution.TransactionStreamer) {
	n.ExecEngine.SetTransactionStreamer(streamer)
}
func (n *ExecutionNode) MessageIndexToBlockNumber(messageNum METAutil.MessageIndex) uint64 {
	return n.ExecEngine.MessageIndexToBlockNumber(messageNum)
}

func (n *ExecutionNode) Maintenance() error {
	return n.ChainDB.Compact(nil, nil)
}
