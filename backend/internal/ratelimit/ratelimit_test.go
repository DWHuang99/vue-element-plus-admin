package ratelimit

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func newTestLimiter(limit int, duration time.Duration) *Limiter {
	return New(Config{Enabled: true, Limit: limit, Duration: duration})
}

func TestAllow_WithinLimit(t *testing.T) {
	l := newTestLimiter(3, time.Minute)
	defer l.Close()

	assert.True(t, l.Allow("key1"))
	assert.True(t, l.Allow("key1"))
	assert.True(t, l.Allow("key1"))
}

func TestAllow_OverLimit(t *testing.T) {
	l := newTestLimiter(2, time.Minute)
	defer l.Close()

	l.Allow("key1")
	l.Allow("key1")
	assert.False(t, l.Allow("key1"), "third request should be blocked")
}

func TestRetryAfter_WhenBlocked(t *testing.T) {
	l := newTestLimiter(1, 10*time.Minute)
	defer l.Close()

	l.Allow("key1")
	assert.False(t, l.Allow("key1"))

	retry := l.RetryAfter("key1")
	assert.Greater(t, retry, time.Duration(0))
	assert.LessOrEqual(t, retry, 10*time.Minute)
}

func TestIndependentKeys(t *testing.T) {
	l := newTestLimiter(1, time.Minute)
	defer l.Close()

	assert.True(t, l.Allow("keyA"))
	assert.False(t, l.Allow("keyA"))

	assert.True(t, l.Allow("keyB"), "different key should be allowed")
}

func TestDisabledAlwaysAllows(t *testing.T) {
	l := New(Config{Enabled: false, Limit: 1, Duration: time.Minute})
	defer l.Close()

	for i := 0; i < 100; i++ {
		assert.True(t, l.Allow("any-key"), "disabled limiter always allows")
	}
}

func TestWindowSlides(t *testing.T) {
	l := newTestLimiter(1, 30*time.Millisecond)
	defer l.Close()

	assert.True(t, l.Allow("key1"))
	assert.False(t, l.Allow("key1"))

	time.Sleep(40 * time.Millisecond)
	assert.True(t, l.Allow("key1"), "after window passes, request allowed again")
}

func TestRetryAfter_WhenNotBlocked(t *testing.T) {
	l := newTestLimiter(5, time.Minute)
	defer l.Close()

	l.Allow("key1")
	assert.Equal(t, time.Duration(0), l.RetryAfter("key1"))
}

func TestNew_ZeroLimitDefaults(t *testing.T) {
	l := New(Config{Enabled: true, Limit: 0, Duration: 0})
	defer l.Close()
	// Defaults applied; Allow works without panic
	l.Allow("key")
}
