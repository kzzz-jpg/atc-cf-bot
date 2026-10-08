package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/internal/store"
)

type Runner interface {
	Run(context.Context, store.JobLease) error
}
type Queue interface {
	ClaimJob(context.Context, string) (store.JobLease, error)
	Heartbeat(context.Context, store.JobLease) error
	FinishJob(context.Context, store.JobLease, string, string, bool) error
}
type Pool struct {
	Store       Queue
	Runner      Runner
	Concurrency int
	Heartbeat   time.Duration
	Log         *slog.Logger
	Notify      func(context.Context, store.JobLease, string)
	wg          sync.WaitGroup
}

func (p *Pool) Start(ctx context.Context) {
	for i := 0; i < p.Concurrency; i++ {
		p.wg.Add(1)
		go p.loop(ctx)
	}
}
func (p *Pool) Wait() { p.wg.Wait() }
func (p *Pool) loop(ctx context.Context) {
	defer p.wg.Done()
	owner := domain.NewID()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			j, e := p.Store.ClaimJob(ctx, owner)
			if e == pgx.ErrNoRows {
				continue
			}
			if e != nil {
				p.Log.Warn("job claim failed", "code", "database")
				continue
			}
			p.run(ctx, j)
		}
	}
}
func (p *Pool) run(root context.Context, j store.JobLease) {
	ctx, cancel := context.WithDeadline(root, j.Deadline)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(p.Heartbeat)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if p.Store.Heartbeat(ctx, j) != nil {
					cancel()
					return
				}
			}
		}
	}()
	e := p.Runner.Run(ctx, j)
	cancel()
	<-done
	status, code, retry := "succeeded", "", false
	if e != nil {
		status = "failed"
		code = "database"
		var de *domain.Error
		if errors.As(e, &de) {
			code = string(de.Code)
			switch de.Code {
			case domain.Ambiguous:
				status = "needs_reconciliation"
			case domain.Unauthorized, domain.Conflict:
				status = "blocked"
			case domain.Transient, domain.RateLimited:
				status = "retry_wait"
				retry = true
			}
		}
		if status != "needs_reconciliation" && time.Now().After(j.Deadline) {
			status = "expired"
		} else if status != "needs_reconciliation" && root.Err() != nil {
			// Graceful interruption does not consume an automatic retry or discard
			// frozen progress. The next run resumes this same durable job.
			status, code, retry = "queued", "interrupted", false
		}
		p.Log.Warn("posting stopped", "job_id", j.ID, "code", code, "status", status)
	}
	clean, c := context.WithTimeout(context.Background(), 5*time.Second)
	if err := p.Store.FinishJob(clean, j, status, code, retry); err != nil {
		p.Log.Warn("job finalization deferred", "job_id", j.ID, "code", "database")
	}
	c()
	if e != nil && p.Notify != nil {
		nctx, nc := context.WithTimeout(root, 10*time.Second)
		p.Notify(nctx, j, code)
		nc()
	}
}
