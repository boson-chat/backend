// Package ratelimit is a keyed token bucket: each key may spend `perMinute`
// tokens a minute, with bursts up to that many.
package ratelimit

import (
	"math"
	"sync"
	"time"
)

type Limiter struct {
	mu        sync.Mutex
	perMinute float64
	now       func() time.Time
	buckets   map[string]*bucket
	ops       int
}

type bucket struct {
	tokens float64
	at     time.Time
}

// New returns a limiter; perMinute <= 0 means unlimited.
func New(perMinute int) *Limiter {
	return &Limiter{perMinute: float64(perMinute), now: time.Now, buckets: map[string]*bucket{}}
}

// Allow spends a token for key. When none is left it returns false and
// how long until the next one.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	if l == nil || l.perMinute <= 0 {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.ops++
	if l.ops%1024 == 0 {
		l.prune(now)
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.perMinute, at: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.perMinute, b.tokens+now.Sub(b.at).Minutes()*l.perMinute)
	b.at = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / l.perMinute * float64(time.Minute))
	return false, wait
}

// prune drops buckets that have refilled (they're the same as new ones).
func (l *Limiter) prune(now time.Time) {
	for k, b := range l.buckets {
		if now.Sub(b.at) >= time.Minute {
			delete(l.buckets, k)
		}
	}
}

func (l *Limiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
