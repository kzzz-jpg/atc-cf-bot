package scheduler

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/internal/store"
)

type scheduleQueue struct {
	recoveries, expirations int
	applied, accepted       []string
}

func (q *scheduleQueue) RecoverExpired(context.Context) error { q.recoveries++; return nil }
func (q *scheduleQueue) Expire(context.Context) error         { q.expirations++; return nil }
func (*scheduleQueue) Guilds(context.Context) ([]string, error) {
	return []string{"not-due", "blocked", "ready"}, nil
}
func (q *scheduleQueue) ApplyPending(_ context.Context, guild string) error {
	q.applied = append(q.applied, guild)
	return nil
}
func (q *scheduleQueue) Accept(_ context.Context, r store.AcceptRequest) (store.JobRef, error) {
	if !r.Scheduled || r.AllowResume || r.Manual {
		panic("scheduler bypassed transactional due check")
	}
	q.accepted = append(q.accepted, r.GuildID)
	switch r.GuildID {
	case "not-due":
		return store.JobRef{}, store.ErrNotDue
	case "blocked":
		return store.JobRef{}, domain.Fail(domain.Conflict)
	}
	return store.JobRef{ID: "ready"}, nil
}
func TestScheduleAdmissionDoesNotSkipOtherGuilds(t *testing.T) {
	q := &scheduleQueue{}
	s := Scheduler{Store: q, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if e := s.Tick(context.Background()); e != nil {
		t.Fatal(e)
	}
	if q.recoveries != 1 || q.expirations != 1 || len(q.applied) != 3 || len(q.accepted) != 3 || q.accepted[2] != "ready" {
		t.Fatalf("guild scheduling stopped early: %+v", q)
	}
}
func TestRecoveryModeDoesNotAdvancePublicWork(t *testing.T) {
	// A nil queue makes any accidental admission or cleanup fail immediately.
	s := Scheduler{RecoveryMode: true}
	if e := s.Tick(context.Background()); e != nil {
		t.Fatal(e)
	}
}
