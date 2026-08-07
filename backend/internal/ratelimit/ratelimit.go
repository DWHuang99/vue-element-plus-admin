package ratelimit

import (
	"sync"
	"time"
)

// window holds a sliding window of request timestamps for one key.
type window struct {
	limit    int
	duration time.Duration
	times    []time.Time
}

// Limiter is an in-memory sliding-window rate limiter.
// Safe for concurrent use. Single-instance only (per plan.md research decision 4).
type Limiter struct {
	mu       sync.Mutex
	enabled  bool
	windows  map[string]*window
	limit    int
	duration time.Duration
	// cleanupInterval controls how often stale buckets are purged.
	cleanupInterval time.Duration
	stop            chan struct{}
}

// Config configures a Limiter.
type Config struct {
	Enabled  bool
	Limit    int           // max requests per duration
	Duration time.Duration // sliding window length
}

// New creates a Limiter. It starts a background goroutine that periodically
// purges stale buckets; call Close to stop it.
func New(cfg Config) *Limiter {
	if cfg.Limit <= 0 {
		cfg.Limit = 1
	}
	if cfg.Duration <= 0 {
		cfg.Duration = time.Minute
	}

	l := &Limiter{
		enabled:         cfg.Enabled,
		windows:         make(map[string]*window),
		limit:           cfg.Limit,
		duration:        cfg.Duration,
		cleanupInterval: cfg.Duration,
		stop:            make(chan struct{}),
	}

	if cfg.Enabled {
		go l.cleanupLoop()
	}
	return l
}

// Close stops the background cleanup goroutine.
func (l *Limiter) Close() {
	close(l.stop)
}

// Allow reports whether a request for key is within the limit.
// When disabled, Allow always returns true.
func (l *Limiter) Allow(key string) bool {
	if !l.enabled {
		return true
	}

	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	w, ok := l.windows[key]
	if !ok {
		w = &window{limit: l.limit, duration: l.duration}
		l.windows[key] = w
	}

	// Drop timestamps outside the sliding window.
	cutoff := now.Add(-w.duration)
	w.times = trimBefore(w.times, cutoff)

	if len(w.times) >= w.limit {
		return false
	}

	w.times = append(w.times, now)
	return true
}

// RetryAfter returns how long the caller must wait before key can be used again.
// Returns 0 if the key is not currently blocked or limiting is disabled.
func (l *Limiter) RetryAfter(key string) time.Duration {
	if !l.enabled {
		return 0
	}

	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	w, ok := l.windows[key]
	if !ok {
		return 0
	}
	cutoff := now.Add(-w.duration)
	w.times = trimBefore(w.times, cutoff)

	if len(w.times) < w.limit {
		return 0
	}

	oldest := w.times[0]
	return oldest.Add(w.duration).Sub(now)
}

// trimBefore removes all timestamps strictly before cutoff, returning a new slice.
func trimBefore(times []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(times) && !times[i].After(cutoff) {
		i++
	}
	if i == 0 {
		return times
	}
	if i >= len(times) {
		return times[:0]
	}
	copy(times, times[i:])
	return times[:len(times)-i]
}

func (l *Limiter) cleanupLoop() {
	ticker := time.NewTicker(l.cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-ticker.C:
			l.purgeStale()
		}
	}
}

func (l *Limiter) purgeStale() {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	for key, w := range l.windows {
		cutoff := now.Add(-w.duration)
		if len(trimBefore(w.times, cutoff)) == 0 {
			delete(l.windows, key)
		}
	}
}
