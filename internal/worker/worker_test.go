package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/internal/store"
)

type queueStub struct {
	heartbeatError        error
	status, code          string
	retry, cleanupWasLive bool
}

func (*queueStub) ClaimJob(context.Context, string) (store.JobLease, error) {
	return store.JobLease{}, pgx.ErrNoRows
}
func (q *queueStub) Heartbeat(context.Context, store.JobLease) error { return q.heartbeatError }
func (q *queueStub) FinishJob(ctx context.Context, _ store.JobLease, status, code string, retry bool) error {
	q.status, q.code, q.retry, q.cleanupWasLive = status, code, retry, ctx.Err() == nil
	return nil
}

type runFunc func(context.Context, store.JobLease) error

func (f runFunc) Run(ctx context.Context, j store.JobLease) error { return f(ctx, j) }

func TestInterruptedJobKeepsRecoverableProgress(t *testing.T) {
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := &queueStub{}
	p := Pool{Store: q, Heartbeat: time.Hour, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	p.Runner = runFunc(func(ctx context.Context, _ store.JobLease) error { cancel(); <-ctx.Done(); return ctx.Err() })
	p.run(root, store.JobLease{Deadline: time.Now().Add(time.Hour)})
	if q.status != "queued" || q.code != "interrupted" || q.retry || !q.cleanupWasLive {
		t.Fatalf("shutdown lost recoverability: %+v", q)
	}
}

func TestExpiredUnknownSendStillRequiresReconciliation(t *testing.T) {
	q := &queueStub{}
	p := Pool{Store: q, Heartbeat: time.Hour, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Runner: runFunc(func(context.Context, store.JobLease) error { return domain.Fail(domain.Ambiguous) })}
	p.run(context.Background(), store.JobLease{Deadline: time.Now().Add(-time.Second)})
	if q.status != "needs_reconciliation" || q.code != "ambiguous" || !q.cleanupWasLive {
		t.Fatalf("unknown send mistaken for expiry: %+v", q)
	}
}

func TestHeartbeatFailureCancelsRunner(t *testing.T) {
	q := &queueStub{heartbeatError: errors.New("connection lost")}
	canceled := false
	p := Pool{Store: q, Heartbeat: time.Millisecond, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Runner: runFunc(func(ctx context.Context, _ store.JobLease) error { <-ctx.Done(); canceled = true; return ctx.Err() })}
	done := make(chan struct{})
	go func() { p.run(context.Background(), store.JobLease{Deadline: time.Now().Add(time.Hour)}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("lost heartbeat did not stop runner")
	}
	if !canceled || !q.cleanupWasLive {
		t.Fatal("runner cancellation or independent cleanup missing")
	}
}
