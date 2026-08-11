package observability

// RecordHTTP (T074) records one completed HTTP request into the registry:
// the total request count, the 401/403/5xx classes (the task's 401/403 ratio
// derives from these) and the end-to-end latency sample. Routers call it
// from their own gin middleware after c.Next() — this package stays gin-free
// (plan.md Architecture: platform imports no gin).
func RecordHTTP(reg *Registry, status int, ms float64) {
	if reg == nil {
		return
	}
	reg.AddCounter("http_requests_total", 1)
	reg.RecordDuration("http_request_duration_ms", ms)
	switch {
	case status == 401:
		reg.AddCounter("http_401_total", 1)
	case status == 403:
		reg.AddCounter("http_403_total", 1)
	case status >= 500:
		reg.AddCounter("http_5xx_total", 1)
	}
	// Ratio gauges stay current on every request.
	if total := reg.CounterValue("http_requests_total"); total > 0 {
		reg.SetGauge("http_401_ratio", float64(reg.CounterValue("http_401_total"))/float64(total))
		reg.SetGauge("http_403_ratio", float64(reg.CounterValue("http_403_total"))/float64(total))
	}
}
