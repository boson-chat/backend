package ratelimit

import (
	"fmt"
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := New(3)
	l.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatalf("burst %d refused", i)
		}
	}
	ok, wait := l.Allow("a")
	if ok || wait <= 0 || wait > 20*time.Second {
		t.Fatalf("4th: ok=%v wait=%v", ok, wait)
	}
	// Other keys have their own bucket.
	if ok, _ := l.Allow("b"); !ok {
		t.Fatal("b refused")
	}
	// 20 s later one token is back.
	now = now.Add(20 * time.Second)
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("not refilled")
	}
	if ok, _ := l.Allow("a"); ok {
		t.Fatal("refilled too much")
	}
}

func TestLimiterUnlimitedAndPrune(t *testing.T) {
	if ok, _ := New(0).Allow("x"); !ok {
		t.Fatal("0 = unlimited")
	}
	var nilL *Limiter
	if ok, _ := nilL.Allow("x"); !ok {
		t.Fatal("nil = unlimited")
	}
	now := time.Unix(0, 0)
	l := New(10)
	l.now = func() time.Time { return now }
	for i := 0; i < 1000; i++ {
		l.Allow(fmt.Sprint(i))
	}
	now = now.Add(2 * time.Minute)
	for i := 0; i < 24; i++ {
		l.Allow("fresh")
	}
	if n := l.size(); n != 1 {
		t.Fatalf("%d buckets after prune", n)
	}
}
