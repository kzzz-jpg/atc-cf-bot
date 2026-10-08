package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/kzzz/atc-cf-bot/internal/store"
)

type Queue interface {
	RecoverExpired(context.Context) error
	Expire(context.Context) error
	Guilds(context.Context) ([]string, error)
	ApplyPending(context.Context, string) error
	Accept(context.Context, store.AcceptRequest) (store.JobRef, error)
}

type Scheduler struct {
	Store        Queue
	Poll         time.Duration
	Log          *slog.Logger
	RecoveryMode bool
}

func (s *Scheduler) Tick(ctx context.Context) error {
	if s.RecoveryMode {
		return nil
	}
	if e := s.Store.RecoverExpired(ctx); e != nil {
		return e
	}
	if e := s.Store.Expire(ctx); e != nil {
		return e
	}
	guilds, e := s.Store.Guilds(ctx)
	if e != nil {
		return e
	}
	for _, guild := range guilds {
		if e = s.Store.ApplyPending(ctx, guild); e != nil {
			return e
		}
		// Admission uses DB time and the day's frozen schedule in one transaction.
		// A cached process clock or latest settings can cross midnight or miss the
		// original due time after a manual publication establishes today's snapshot.
		if _, e = s.Store.Accept(ctx, store.AcceptRequest{GuildID: guild, Scheduled: true}); e != nil && !errors.Is(e, store.ErrNotDue) {
			s.Log.Warn("schedule not accepted", "guild_id", guild, "code", "blocked_or_database")
		}
	}
	return nil
}
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.Poll)
	defer ticker.Stop()
	for {
		if e := s.Tick(ctx); e != nil {
			s.Log.Warn("scheduler unavailable", "code", "database")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
