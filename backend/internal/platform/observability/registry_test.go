package observability

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryCountersAndGauges(t *testing.T) {
	reg := NewRegistry()
	reg.AddCounter("a_total", 1)
	reg.AddCounter("a_total", 2)
	assert.Equal(t, int64(3), reg.CounterValue("a_total"))
	assert.Equal(t, int64(0), reg.CounterValue("never"))

	reg.SetGauge("g", 4.5)
	reg.SetGauge("g", 2.0)
}

func TestRegistryDurationsExpandToThreeMetrics(t *testing.T) {
	reg := NewRegistry()
	reg.RecordDuration("op_ms", 10)
	reg.RecordDuration("op_ms", 30)

	snap := reg.Snapshot()
	got := map[string]Metric{}
	for _, m := range snap {
		got[m.Name] = m
	}
	assert.Equal(t, float64(2), got["op_ms_count"].Value)
	assert.Equal(t, float64(40), got["op_ms_sum_ms"].Value)
	assert.Equal(t, float64(20), got["op_ms_avg_ms"].Value)
	assert.Equal(t, KindCounter, got["op_ms_count"].Kind)
	assert.Equal(t, KindGauge, got["op_ms_avg_ms"].Kind)
}

func TestRegistrySnapshotDeterministicOrder(t *testing.T) {
	reg := NewRegistry()
	reg.AddCounter("zeta_total", 1)
	reg.SetGauge("alpha", 1)
	reg.RecordDuration("mid_ms", 5)

	snap := reg.Snapshot()

	// Plain counters/gauges appear globally sorted; the duration triplet
	// (mid_ms) is one contiguous unit in count/sum/avg order at its name's
	// sorted position.
	plain := make([]string, 0, len(snap))
	seen := map[string]bool{}
	for i, m := range snap {
		if m.Name == "mid_ms_count" {
			require.True(t, i+2 < len(snap), "duration triplet contiguous")
			assert.Equal(t, "mid_ms_sum_ms", snap[i+1].Name)
			assert.Equal(t, "mid_ms_avg_ms", snap[i+2].Name)
		}
		if m.Name == "mid_ms_count" || m.Name == "mid_ms_sum_ms" || m.Name == "mid_ms_avg_ms" {
			continue
		}
		if !seen[m.Name] {
			plain = append(plain, m.Name)
			seen[m.Name] = true
		}
	}
	for i := 1; i < len(plain); i++ {
		assert.Less(t, plain[i-1], plain[i], "snapshot names must be sorted")
	}

	again := reg.Snapshot()
	require.Equal(t, len(snap), len(again))
	for i := range snap {
		assert.Equal(t, snap[i], again[i], "snapshot must be deterministic")
	}
}

// polledCollector is a test Collector that bumps a counter on each Collect.
type polledCollector struct{ calls int }

func (c *polledCollector) Collect(ctx context.Context, reg *Registry) {
	c.calls++
	reg.AddCounter("collector_runs_total", 1)
}

func TestRegistryCollectRunsPolledCollectors(t *testing.T) {
	reg := NewRegistry()
	c := &polledCollector{}
	reg.RegisterCollector(c)
	reg.RegisterCollector(c)

	reg.Collect(context.Background())
	reg.Collect(context.Background())
	assert.Equal(t, 4, c.calls)
	assert.Equal(t, int64(4), reg.CounterValue("collector_runs_total"))
}

func TestHandlerServesTextLines(t *testing.T) {
	reg := NewRegistry()
	reg.AddCounter("http_requests_total", 3)
	reg.SetGauge("provisioning_count", 2)
	reg.RecordDuration("op_ms", 5)

	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
	body := rec.Body.String()
	for _, want := range []string{
		"http_requests_total 3\n",
		"provisioning_count 2\n",
		"op_ms_count 1\n",
		"op_ms_sum_ms 5\n",
		"op_ms_avg_ms 5\n",
	} {
		assert.Contains(t, body, want)
	}
	// formatValue prints integer-valued metrics without decimals; the body
	// ends with exactly one newline (no blank trailing line).
	assert.NotContains(t, body, "3.000")
	assert.True(t, strings.HasSuffix(body, "\n"))
	assert.False(t, strings.HasSuffix(body, "\n\n"), "no trailing blank line")
}

func TestFormatValue(t *testing.T) {
	assert.Equal(t, "7", formatValue(Metric{Value: 7}))
	assert.Equal(t, "0", formatValue(Metric{Value: 0}))
	assert.Equal(t, "2.500", formatValue(Metric{Value: 2.5}))
	assert.Equal(t, fmt.Sprintf("%.3f", 0.1), formatValue(Metric{Value: 0.1}))
}
