package generation

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type memoryCache struct {
	mu     sync.Mutex
	result map[string]json.RawMessage
}

func (m *memoryCache) Cached(_ context.Context, k string) (json.RawMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b := m.result[k]; b != nil {
		return b, nil
	}
	return nil, errors.New("missing")
}
func (*memoryCache) BeginGeneration(context.Context, string, string, string, string, any) error {
	return nil
}
func (*memoryCache) GenerationHeartbeat(context.Context, string, string) error { return nil }
func (m *memoryCache) EndGeneration(_ context.Context, k, _ string, b json.RawMessage, e error) error {
	if e == nil {
		m.mu.Lock()
		m.result[k] = b
		m.mu.Unlock()
	}
	return nil
}
func TestSharedContextSurvivesFirstWaiter(t *testing.T) {
	root, cancel := context.WithCancel(context.Background())
	cache := &memoryCache{result: map[string]json.RawMessage{}}
	c := New(root, cache, 2, 20, 200, 50, time.Second)
	defer func() { cancel(); c.Close() }()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	fn := func(ctx context.Context) (json.RawMessage, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return json.RawMessage(`{"text":"ok"}`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	first, leave := context.WithCancel(root)
	firstDone := make(chan error, 1)
	go func() { _, e := c.Get(first, "g", "key", "s", "hint", nil, false, fn); firstDone <- e }()
	<-started
	secondDone := make(chan error, 1)
	go func() { _, e := c.Get(root, "g", "key", "s", "hint", nil, false, fn); secondDone <- e }()
	deadline := time.Now().Add(time.Second)
	for {
		c.mu.Lock()
		joined := c.entries["key"].waiters == 2
		c.mu.Unlock()
		if joined {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second waiter never joined")
		}
		time.Sleep(time.Millisecond)
	}
	leave()
	if <-firstDone == nil {
		t.Fatal("cancelled waiter succeeded")
	}
	close(release)
	if e := <-secondDone; e != nil || calls.Load() != 1 {
		t.Fatalf("shared work cancelled: %v calls %d", e, calls.Load())
	}
	if _, e := c.Get(root, "g", "key", "s", "hint", nil, false, fn); e != nil || calls.Load() != 1 {
		t.Fatal("cache miss")
	}
}
func TestBoundedWaiters(t *testing.T) {
	root, cancel := context.WithCancel(context.Background())
	c := New(root, &memoryCache{result: map[string]json.RawMessage{}}, 1, 2, 1, 1, time.Second)
	defer func() { cancel(); c.Close() }()
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		_, _ = c.Get(root, "g", "a", "s", "hint", nil, false, func(ctx context.Context) (json.RawMessage, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		})
		close(done)
	}()
	<-started
	if _, e := c.Get(root, "g", "b", "s", "hint", nil, false, func(context.Context) (json.RawMessage, error) { t.Fatal("rejected request executed"); return nil, nil }); e == nil {
		t.Fatal("waiter limit ignored")
	}
	cancel()
	<-done
}
func TestFairQueues(t *testing.T) {
	c := &Coordinator{wake: make(chan struct{}, 1)}
	for i := 0; i < 10; i++ {
		c.private = append(c.private, &task{kind: "private"})
	}
	c.public = []*task{{kind: "public"}}
	for i := 0; i < 3; i++ {
		if c.take().kind != "private" {
			t.Fatal("wrong selection")
		}
	}
	if c.take().kind != "public" {
		t.Fatal("public queue starved")
	}
}
