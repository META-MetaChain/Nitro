package sharedmetrics

import (
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/META-MetaChain/nitro/METAutil"
)

var (
	latestSequenceNumberGauge  = metrics.NewRegisteredGauge("META/sequencenumber/latest", nil)
	sequenceNumberInBlockGauge = metrics.NewRegisteredGauge("META/sequencenumber/inblock", nil)
)

func UpdateSequenceNumberGauge(sequenceNumber METAutil.MessageIndex) {
	latestSequenceNumberGauge.Update(int64(sequenceNumber))
}
func UpdateSequenceNumberInBlockGauge(sequenceNumber METAutil.MessageIndex) {
	sequenceNumberInBlockGauge.Update(int64(sequenceNumber))
}
