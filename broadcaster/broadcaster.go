// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

package broadcaster

import (
	"context"
	"net"

	"github.com/gobwas/ws"

	"github.com/ethereum/go-ethereum/log"

	"github.com/META-MetaChain/nitro/METAos/METAostypes"
	"github.com/META-MetaChain/nitro/METAutil"
	"github.com/META-MetaChain/nitro/broadcaster/backlog"
	m "github.com/META-MetaChain/nitro/broadcaster/message"
	"github.com/META-MetaChain/nitro/util/signature"
	"github.com/META-MetaChain/nitro/wsbroadcastserver"
)

type Broadcaster struct {
	server     *wsbroadcastserver.WSBroadcastServer
	backlog    backlog.Backlog
	chainId    uint64
	dataSigner signature.DataSignerFunc
}

func NewBroadcaster(config wsbroadcastserver.BroadcasterConfigFetcher, chainId uint64, feedErrChan chan error, dataSigner signature.DataSignerFunc) *Broadcaster {
	bklg := backlog.NewBacklog(func() *backlog.Config { return &config().Backlog })
	return &Broadcaster{
		server:     wsbroadcastserver.NewWSBroadcastServer(config, bklg, chainId, feedErrChan),
		backlog:    bklg,
		chainId:    chainId,
		dataSigner: dataSigner,
	}
}

func (b *Broadcaster) NewBroadcastFeedMessage(message METAostypes.MessageWithMetadata, sequenceNumber METAutil.MessageIndex) (*m.BroadcastFeedMessage, error) {
	var messageSignature []byte
	if b.dataSigner != nil {
		hash, err := message.Hash(sequenceNumber, b.chainId)
		if err != nil {
			return nil, err
		}
		messageSignature, err = b.dataSigner(hash.Bytes())
		if err != nil {
			return nil, err
		}
	}

	return &m.BroadcastFeedMessage{
		SequenceNumber: sequenceNumber,
		Message:        message,
		Signature:      messageSignature,
	}, nil
}

func (b *Broadcaster) BroadcastSingle(msg METAostypes.MessageWithMetadata, seq METAutil.MessageIndex) error {
	defer func() {
		if r := recover(); r != nil {
			log.Error("recovered error in BroadcastSingle", "recover", r)
		}
	}()
	bfm, err := b.NewBroadcastFeedMessage(msg, seq)
	if err != nil {
		return err
	}

	b.BroadcastSingleFeedMessage(bfm)
	return nil
}

func (b *Broadcaster) BroadcastSingleFeedMessage(bfm *m.BroadcastFeedMessage) {
	broadcastFeedMessages := make([]*m.BroadcastFeedMessage, 0, 1)

	broadcastFeedMessages = append(broadcastFeedMessages, bfm)

	b.BroadcastFeedMessages(broadcastFeedMessages)
}

func (b *Broadcaster) BroadcastFeedMessages(messages []*m.BroadcastFeedMessage) {

	bm := &m.BroadcastMessage{
		Version:  1,
		Messages: messages,
	}

	b.server.Broadcast(bm)
}

func (b *Broadcaster) Confirm(seq METAutil.MessageIndex) {
	log.Debug("confirming sequence number", "sequenceNumber", seq)
	b.server.Broadcast(&m.BroadcastMessage{
		Version: 1,
		ConfirmedSequenceNumberMessage: &m.ConfirmedSequenceNumberMessage{
			SequenceNumber: seq,
		},
	})
}

func (b *Broadcaster) ClientCount() int32 {
	return b.server.ClientCount()
}

func (b *Broadcaster) ListenerAddr() net.Addr {
	return b.server.ListenerAddr()
}

func (b *Broadcaster) GetCachedMessageCount() int {
	return int(b.backlog.Count())
}

func (b *Broadcaster) Initialize() error {
	return b.server.Initialize()
}

func (b *Broadcaster) Start(ctx context.Context) error {
	return b.server.Start(ctx)
}

func (b *Broadcaster) StartWithHeader(ctx context.Context, header ws.HandshakeHeader) error {
	return b.server.StartWithHeader(ctx, header)
}

func (b *Broadcaster) StopAndWait() {
	b.server.StopAndWait()
}

func (b *Broadcaster) Started() bool {
	return b.server.Started()
}
