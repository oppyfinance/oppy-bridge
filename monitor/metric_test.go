package monitor

import (
	"testing"
)

func TestMetricInbound(t *testing.T) {
	metrics := NewMetric()
	metrics.UpdateInboundTxNum(1)
}

func TestMetricOutbound(t *testing.T) {
	metrics := NewMetric()
	metrics.UpdateOutboundTxNum(1)
}
