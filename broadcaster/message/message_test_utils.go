package message

import (
	"github.com/META-MetaChain/nitro/METAos/METAostypes"
	"github.com/META-MetaChain/nitro/METAutil"
)

func CreateDummyBroadcastMessage(seqNums []METAutil.MessageIndex) *BroadcastMessage {
	return &BroadcastMessage{
		Messages: CreateDummyBroadcastMessages(seqNums),
	}
}

func CreateDummyBroadcastMessages(seqNums []METAutil.MessageIndex) []*BroadcastFeedMessage {
	return CreateDummyBroadcastMessagesImpl(seqNums, len(seqNums))
}

func CreateDummyBroadcastMessagesImpl(seqNums []METAutil.MessageIndex, length int) []*BroadcastFeedMessage {
	broadcastMessages := make([]*BroadcastFeedMessage, 0, length)
	for _, seqNum := range seqNums {
		broadcastMessage := &BroadcastFeedMessage{
			SequenceNumber: seqNum,
			Message:        METAostypes.EmptyTestMessageWithMetadata,
		}
		broadcastMessages = append(broadcastMessages, broadcastMessage)
	}

	return broadcastMessages
}
