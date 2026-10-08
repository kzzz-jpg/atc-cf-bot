package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kzzz/atc-cf-bot/internal/domain"
)

type GuildConfig struct {
	GuildID  string
	Version  int64
	Settings domain.Settings
}

func (s *Store) EnsureGuild(ctx context.Context, guild string) error {
	if !domain.Snowflake(guild) {
		return domain.Fail(domain.Invalid)
	}
	raw, _ := json.Marshal(domain.DefaultSettings())
	return s.transaction(ctx, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, "INSERT INTO guild_configs(guild_id,version,settings) VALUES($1,1,$2) ON CONFLICT DO NOTHING", guild, raw); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, "INSERT INTO guild_runtime(guild_id) VALUES($1) ON CONFLICT DO NOTHING", guild)
		return e
	})
}
func (s *Store) Config(ctx context.Context, guild string) (GuildConfig, error) {
	var cfg GuildConfig
	var raw []byte
	cfg.GuildID = guild
	e := s.Pool.QueryRow(ctx, "SELECT version,settings FROM guild_configs WHERE guild_id=$1", guild).Scan(&cfg.Version, &raw)
	if e != nil {
		return cfg, e
	}
	e = json.Unmarshal(raw, &cfg.Settings)
	return cfg, e
}
func (s *Store) Guilds(ctx context.Context) ([]string, error) {
	rows, e := s.Pool.Query(ctx, "SELECT guild_id FROM guild_configs ORDER BY guild_id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
func (s *Store) UpdateConfig(ctx context.Context, guild string, expected int64, next domain.Settings) (bool, error) {
	if e := next.Validate(); e != nil {
		return false, e
	}
	pending := false
	e := s.transaction(ctx, func(tx pgx.Tx) error {
		var raw []byte
		var version int64
		var now time.Time
		e := tx.QueryRow(ctx, "SELECT version,settings,clock_timestamp() FROM guild_configs WHERE guild_id=$1 FOR UPDATE", guild).Scan(&version, &raw, &now)
		if e != nil {
			return e
		}
		if version != expected {
			return domain.Fail(domain.Conflict)
		}
		var old domain.Settings
		if e = json.Unmarshal(raw, &old); e != nil {
			return e
		}
		var existingPending []byte
		var effective *time.Time
		if e = tx.QueryRow(ctx, "SELECT pending_schedule,pending_effective_at FROM guild_configs WHERE guild_id=$1", guild).Scan(&existingPending, &effective); e != nil {
			return e
		}
		cycle := domain.CycleAt(now, old)
		var desired []byte
		if next.Timezone != old.Timezone {
			pending = true
			desired, _ = json.Marshal(map[string]string{"timezone": next.Timezone})
			next.Timezone = old.Timezone
			if effective == nil {
				effective = &cycle.End
			}
		}
		if !pending && existingPending != nil {
			desired = existingPending
			pending = true
		}
		raw, _ = json.Marshal(next)
		_, e = tx.Exec(ctx, "UPDATE guild_configs SET version=version+1,settings=$3,pending_schedule=$4,pending_effective_at=$5,updated_at=clock_timestamp() WHERE guild_id=$1 AND version=$2", guild, expected, raw, desired, effective)
		return e
	})
	return pending, e
}
func (s *Store) ApplyPending(ctx context.Context, guild string) error {
	return s.transaction(ctx, func(tx pgx.Tx) error {
		var raw, pending []byte
		var effective *time.Time
		var now time.Time
		e := tx.QueryRow(ctx, "SELECT settings,pending_schedule,pending_effective_at,clock_timestamp() FROM guild_configs WHERE guild_id=$1 FOR UPDATE", guild).Scan(&raw, &pending, &effective, &now)
		if e != nil {
			return e
		}
		if effective == nil || now.Before(*effective) {
			return nil
		}
		var cfg domain.Settings
		var patch map[string]string
		if json.Unmarshal(raw, &cfg) != nil || json.Unmarshal(pending, &patch) != nil {
			return domain.Fail(domain.Configuration)
		}
		cfg.Timezone = patch["timezone"]
		raw, _ = json.Marshal(cfg)
		_, e = tx.Exec(ctx, "UPDATE guild_configs SET settings=$2,pending_schedule=NULL,pending_effective_at=NULL,version=version+1,updated_at=clock_timestamp() WHERE guild_id=$1", guild, raw)
		return e
	})
}
