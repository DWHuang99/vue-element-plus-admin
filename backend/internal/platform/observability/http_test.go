package observability

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRecordHTTPNilRegistrySafe(t *testing.T) {
	// Must not panic.
	RecordHTTP(nil, 200, 1.0)
}

func TestRecordHTTPSuccess(t *testing.T) {
	reg := NewRegistry()
	RecordHTTP(reg, 200, 12.5)

	assert.Equal(t, int64(1), reg.CounterValue("http_requests_total"))
	assert.Equal(t, int64(0), reg.CounterValue("http_401_total"))
	assert.Equal(t, int64(0), reg.CounterValue("http_403_total"))
	assert.Equal(t, int64(0), reg.CounterValue("http_5xx_total"))
	assert.Equal(t, float64(0), gaugeValue(t, reg, "http_401_ratio"))
	assert.Equal(t, float64(0), gaugeValue(t, reg, "http_403_ratio"))

	snap := metricByName(t, reg, "http_request_duration_ms_count")
	assert.Equal(t, float64(1), snap.Value)
}

func TestRecordHTTPErrorClasses(t *testing.T) {
	reg := NewRegistry()
	RecordHTTP(reg, 401, 1)
	RecordHTTP(reg, 401, 1)
	RecordHTTP(reg, 403, 1)
	RecordHTTP(reg, 503, 1)
	RecordHTTP(reg, 500, 1)
	RecordHTTP(reg, 200, 1)

	assert.Equal(t, int64(6), reg.CounterValue("http_requests_total"))
	assert.Equal(t, int64(2), reg.CounterValue("http_401_total"))
	assert.Equal(t, int64(1), reg.CounterValue("http_403_total"))
	assert.Equal(t, int64(2), reg.CounterValue("http_5xx_total"))
	// 401/403 ratios are live current values: 2/6 and 1/6.
	assert.Equal(t, float64(2)/6, gaugeValue(t, reg, "http_401_ratio"))
	assert.Equal(t, float64(1)/6, gaugeValue(t, reg, "http_403_ratio"))
}

func gaugeValue(t *testing.T, reg *Registry, name string) float64 {
	t.Helper()
	for _, m := range reg.Snapshot() {
		if m.Name == name {
			return m.Value
		}
	}
	t.Fatalf("gauge %s not present", name)
	return 0
}

func metricByName(t *testing.T, reg *Registry, name string) Metric {
	t.Helper()
	for _, m := range reg.Snapshot() {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("metric %s not present", name)
	return Metric{}
}
