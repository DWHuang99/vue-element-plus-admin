// Package observability (T074, plan Phase 7.5) provides the in-process
// metrics registry and its HTTP exposition. No tracing/metrics dependency is
// introduced (plan.md Observability: OTel only when directly pinned); the
// registry is a bounded set of monotonic counters, current-value gauges and
// duration samples that a Prometheus collector could later scrape from the
// text exposition without protocol changes.
//
// Boundary rules (plan.md Architecture): this package stays gin/pgx/sqlc/
// database-free — routers adapt it with a one-line gin handler.
package observability

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Kind classifies one Metric in a snapshot.
type Kind uint8

const (
	// KindCounter is a monotonic cumulative count.
	KindCounter Kind = iota
	// KindGauge is a current value (may go up and down).
	KindGauge
)

// Metric is one exposed value. Durations expand into three metrics at
// snapshot time: <name>_count, <name>_sum_ms and <name>_avg_ms.
type Metric struct {
	Name  string
	Kind  Kind
	Value float64
}

// durationStat accumulates (count, sum-ms) pairs for average latency.
type durationStat struct {
	count int64
	sumMS float64
}

// Registry is a thread-safe metric store. Collectors registered with
// RegisterCollector are run by Collect (triggered by the /metrics handler)
// so every snapshot reflects fresh state without a background loop.
type Registry struct {
	mu        sync.Mutex
	counters  map[string]int64
	gauges    map[string]float64
	durations map[string]*durationStat
	collector []Collector
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		counters:  map[string]int64{},
		gauges:    map[string]float64{},
		durations: map[string]*durationStat{},
	}
}

// Collector is one polled metric source (DB-derived gauges, counters).
// Collect must only mutate the registry — never block on network beyond the
// ctx deadline.
type Collector interface {
	Collect(ctx context.Context, reg *Registry)
}

// RegisterCollector appends a polled source.
func (r *Registry) RegisterCollector(c Collector) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.collector = append(r.collector, c)
}

// AddCounter increments a monotonic counter by delta.
func (r *Registry) AddCounter(name string, delta int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters[name] += delta
}

// CounterValue reads one counter without a full snapshot (hot path).
func (r *Registry) CounterValue(name string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counters[name]
}

// SetGauge sets a current-value gauge.
func (r *Registry) SetGauge(name string, value float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gauges[name] = value
}

// RecordDuration appends one latency sample (milliseconds).
func (r *Registry) RecordDuration(name string, ms float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.durations[name]
	if d == nil {
		d = &durationStat{}
		r.durations[name] = d
	}
	d.count++
	d.sumMS += ms
}

// Collect runs every registered collector once (a /metrics scrape pass).
func (r *Registry) Collect(ctx context.Context) {
	r.mu.Lock()
	collectors := append([]Collector(nil), r.collector...)
	r.mu.Unlock()
	for _, c := range collectors {
		c.Collect(ctx, r)
	}
}

// Snapshot returns every metric deterministically ordered by name (durations
// expand inline to <name>_count / <name>_sum_ms / <name>_avg_ms at their
// name's sorted position).
func (r *Registry) Snapshot() []Metric {
	r.mu.Lock()
	defer r.mu.Unlock()

	names := make([]string, 0, len(r.counters)+len(r.gauges)+len(r.durations))
	for name := range r.counters {
		names = append(names, name)
	}
	for name := range r.gauges {
		names = append(names, name)
	}
	for name := range r.durations {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]Metric, 0, len(names)+3*len(r.durations))
	for _, name := range names {
		if v, ok := r.counters[name]; ok {
			out = append(out, Metric{Name: name, Kind: KindCounter, Value: float64(v)})
		}
		if v, ok := r.gauges[name]; ok {
			out = append(out, Metric{Name: name, Kind: KindGauge, Value: v})
		}
		if d, ok := r.durations[name]; ok {
			out = append(out,
				Metric{Name: name + "_count", Kind: KindCounter, Value: float64(d.count)},
				Metric{Name: name + "_sum_ms", Kind: KindCounter, Value: d.sumMS},
				Metric{Name: name + "_avg_ms", Kind: KindGauge, Value: avgOrZero(d.sumMS, d.count)},
			)
		}
	}
	return out
}

func avgOrZero(sum float64, count int64) float64 {
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}

// Handler exposes GET /metrics as plain-text `name value` lines (scrape-safe,
// sorted). Collectors run before each snapshot so the response is fresh.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
		defer cancel()
		r.Collect(ctx)

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		for _, m := range r.Snapshot() {
			fmt.Fprintf(w, "%s %s\n", m.Name, formatValue(m))
		}
	})
}

func formatValue(m Metric) string {
	if m.Value == float64(int64(m.Value)) {
		return fmt.Sprintf("%d", int64(m.Value))
	}
	return fmt.Sprintf("%.3f", m.Value)
}
