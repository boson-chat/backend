package unfurl

import (
	"container/list"
	"sync"
	"time"
)

// lru is a bounded least-recently-used cache whose entries expire: hits
// after ttl, misses (a nil value: no preview) after negTTL, so a transient
// failure recovers sooner.
type lru[T any] struct {
	mu          sync.Mutex
	max         int
	ttl, negTTL time.Duration
	now         func() time.Time
	order       *list.List // front = most recent
	items       map[string]*list.Element
}

type lruEntry[T any] struct {
	key string
	at  time.Time
	val *T
}

func newLRU[T any](max int, ttl, negTTL time.Duration) *lru[T] {
	if max < 1 {
		max = 1
	}
	return &lru[T]{max: max, ttl: ttl, negTTL: negTTL, now: time.Now, order: list.New(), items: map[string]*list.Element{}}
}

func (c *lru[T]) get(k string) (*T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[k]
	if !ok {
		return nil, false
	}
	e := el.Value.(*lruEntry[T])
	ttl := c.ttl
	if e.val == nil {
		ttl = c.negTTL
	}
	if c.now().Sub(e.at) >= ttl {
		c.order.Remove(el)
		delete(c.items, k)
		return nil, false
	}
	c.order.MoveToFront(el)
	return e.val, true
}

func (c *lru[T]) put(k string, v *T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[k]; ok {
		el.Value = &lruEntry[T]{key: k, at: c.now(), val: v}
		c.order.MoveToFront(el)
		return
	}
	c.items[k] = c.order.PushFront(&lruEntry[T]{key: k, at: c.now(), val: v})
	for c.order.Len() > c.max {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.items, last.Value.(*lruEntry[T]).key)
	}
}

func (c *lru[T]) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// flight runs one fetch per key at a time; concurrent askers share it.
type flight[T any] struct {
	mu    sync.Mutex
	calls map[string]*flightCall[T]
}

type flightCall[T any] struct {
	done chan struct{}
	val  *T
}

func (f *flight[T]) do(k string, fn func() *T) *T {
	f.mu.Lock()
	if f.calls == nil {
		f.calls = map[string]*flightCall[T]{}
	}
	if c, ok := f.calls[k]; ok {
		f.mu.Unlock()
		<-c.done
		return c.val
	}
	c := &flightCall[T]{done: make(chan struct{})}
	f.calls[k] = c
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.calls, k)
		f.mu.Unlock()
		close(c.done)
	}()
	c.val = fn()
	return c.val
}
