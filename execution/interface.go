package execution

import (
	"context"
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/META-MetaChain/nitro/METAos/METAostypes"
	"github.com/META-MetaChain/nitro/METAutil"
	"github.com/META-MetaChain/nitro/validator"
)

type MessageResult struct {
	BlockHash common.Hash
	SendRoot  common.Hash
}

type RecordResult struct {
	Pos       METAutil.MessageIndex
	BlockHash common.Hash
	Preimages map[common.Hash][]byte
	BatchInfo []validator.BatchInfo
}

var ErrRetrySequencer = errors.New("please retry transaction")
var ErrSequencerInsertLockTaken = errors.New("insert lock taken")

// always needed
type ExecutionClient interface {
	DigestMessage(num METAutil.MessageIndex, msg *METAostypes.MessageWithMetadata) error
	Reorg(count METAutil.MessageIndex, newMessages []METAostypes.MessageWithMetadata, oldMessages []*METAostypes.MessageWithMetadata) error
	HeadMessageNumber() (METAutil.MessageIndex, error)
	HeadMessageNumberSync(t *testing.T) (METAutil.MessageIndex, error)
	ResultAtPos(pos METAutil.MessageIndex) (*MessageResult, error)
}

// needed for validators / stakers
type ExecutionRecorder interface {
	RecordBlockCreation(
		ctx context.Context,
		pos METAutil.MessageIndex,
		msg *METAostypes.MessageWithMetadata,
	) (*RecordResult, error)
	MarkValid(pos METAutil.MessageIndex, resultHash common.Hash)
	PrepareForRecord(ctx context.Context, start, end METAutil.MessageIndex) error
}

// needed for sequencer
type ExecutionSequencer interface {
	ExecutionClient
	Pause()
	Activate()
	ForwardTo(url string) error
	SequenceDelayedMessage(message *METAostypes.L1IncomingMessage, delayedSeqNum uint64) error
	NextDelayedMessageNumber() (uint64, error)
	SetTransactionStreamer(streamer TransactionStreamer)
}

type FullExecutionClient interface {
	ExecutionClient
	ExecutionRecorder
	ExecutionSequencer

	Start(ctx context.Context) error
	StopAndWait()

	Maintenance() error

	// TODO: only used to get safe/finalized block numbers
	MessageIndexToBlockNumber(messageNum METAutil.MessageIndex) uint64
}

// not implemented in execution, used as input
type BatchFetcher interface {
	FetchBatch(batchNum uint64) ([]byte, error)
}

type TransactionStreamer interface {
	BatchFetcher
	WriteMessageFromSequencer(pos METAutil.MessageIndex, msgWithMeta METAostypes.MessageWithMetadata) error
	ExpectChosenSequencer() error
}
