package generation

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/kzzz/atc-cf-bot/internal/domain"
)

type Cache interface {
	Cached(context.Context, string) (json.RawMessage, error)
	BeginGeneration(context.Context, string, string, string, string, any) error
	GenerationHeartbeat(context.Context, string, string) error
	EndGeneration(context.Context, string, string, json.RawMessage, error) error
}
type task struct {
	key, sid, kind, owner string
	fp                    any
	public                bool
	fn                    func(context.Context) (json.RawMessage, error)
	ctx                   context.Context
	cancel                context.CancelFunc
	done                  chan struct{}
	result                json.RawMessage
	err                   error
	waiters               int
}
type Coordinator struct {
	root                                context.Context
	cache                               Cache
	timeout                             time.Duration
	queueLimit, waiterLimit, guildLimit int
	mu                                  sync.Mutex
	entries                             map[string]*task
	guildWaiters                        map[string]int
	waiters                             int
	public, private                     []*task
	wake                                chan struct{}
	wg                                  sync.WaitGroup
	burst                               int
}

func New(ctx context.Context, cache Cache, concurrency, queue, waiters, guildWaiters int, timeout time.Duration) *Coordinator {
	c := &Coordinator{root: ctx, cache: cache, timeout: timeout, queueLimit: queue, waiterLimit: waiters, guildLimit: guildWaiters, entries: map[string]*task{}, guildWaiters: map[string]int{}, wake: make(chan struct{}, 1)}
	for i := 0; i < concurrency; i++ {
		c.wg.Add(1)
		go c.worker()
	}
	return c
}
func (c *Coordinator) Get(ctx context.Context, guild, key, sid, kind string, fp any, public bool, fn func(context.Context) (json.RawMessage, error)) (json.RawMessage, error) {
	// Limit subscriptions even for fast cache hits; router applies cooldown first.
	c.mu.Lock()
	if c.waiters >= c.waiterLimit || c.guildWaiters[guild] >= c.guildLimit {
		c.mu.Unlock()
		return nil, domain.Fail(domain.RateLimited)
	}
	c.waiters++
	c.guildWaiters[guild]++
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.waiters--; c.guildWaiters[guild]--; c.mu.Unlock() }()
	if result, e := c.cache.Cached(ctx, key); e == nil {
		return result, nil
	}
	c.mu.Lock()
	t := c.entries[key]
	if t == nil {
		if c.root.Err() != nil || len(c.private)+len(c.public) >= c.queueLimit {
			c.mu.Unlock()
			return nil, domain.Fail(domain.RateLimited)
		}
		shared, cancel := context.WithTimeout(c.root, c.timeout)
		t = &task{key: key, sid: sid, kind: kind, owner: domain.NewID(), fp: fp, public: public, fn: fn, ctx: shared, cancel: cancel, done: make(chan struct{})}
		c.entries[key] = t
		if public {
			c.public = append(c.public, t)
		} else {
			c.private = append(c.private, t)
		}
		c.signal()
	}
	t.waiters++
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		t.waiters--
		if t.waiters == 0 {
			t.cancel()
		}
		c.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return nil, domain.Fail(domain.Transient)
	case <-t.done:
		return t.result, t.err
	}
}
func (c *Coordinator) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}
func (c *Coordinator) take() *task {
	c.mu.Lock()
	defer c.mu.Unlock()
	var t *task
	if len(c.public) > 0 && (len(c.private) == 0 || c.burst >= 3) {
		t = c.public[0]
		c.public = c.public[1:]
		c.burst = 0
	} else if len(c.private) > 0 {
		t = c.private[0]
		c.private = c.private[1:]
		c.burst++
	}
	if len(c.public)+len(c.private) > 0 {
		c.signal()
	}
	return t
}
func (c *Coordinator) worker() {
	defer c.wg.Done()
	for {
		if c.root.Err() != nil {
			return
		}
		t := c.take()
		if t == nil {
			select {
			case <-c.root.Done():
				return
			case <-c.wake:
				continue
			}
		}
		c.execute(t)
	}
}
func (c *Coordinator) execute(t *task) {
	defer t.cancel()
	e := c.cache.BeginGeneration(t.ctx, t.key, t.sid, t.kind, t.owner, t.fp)
	var result json.RawMessage
	if e == nil {
		heartbeatDone := make(chan struct{})
		go func() {
			defer close(heartbeatDone)
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-t.ctx.Done():
					return
				case <-ticker.C:
					if c.cache.GenerationHeartbeat(t.ctx, t.key, t.owner) != nil {
						t.cancel()
						return
					}
				}
			}
		}()
		result, e = t.fn(t.ctx)
		t.cancel()
		<-heartbeatDone
		clean, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		save := c.cache.EndGeneration(clean, t.key, t.owner, result, e)
		cancel()
		if save != nil {
			e = domain.Fail(domain.Transient)
		}
	}
	c.mu.Lock()
	t.result = result
	t.err = e
	delete(c.entries, t.key)
	close(t.done)
	c.mu.Unlock()
}
func (c *Coordinator) Close() {
	c.wg.Wait()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range c.entries {
		t.cancel()
		t.err = domain.Fail(domain.Transient)
		close(t.done)
		delete(c.entries, t.key)
	}
}
