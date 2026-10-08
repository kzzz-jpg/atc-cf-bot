package store

import (
	"context"
	"encoding/json"

	"github.com/kzzz/atc-cf-bot/internal/domain"
)

func (s *Store) Cached(ctx context.Context, key string) (json.RawMessage, error) {
	var b []byte
	e := s.Pool.QueryRow(ctx, "SELECT result FROM generation_entries WHERE cache_key=$1 AND status='ready'", key).Scan(&b)
	return b, e
}
func (s *Store) BeginGeneration(ctx context.Context, key, sid, kind, owner string, fp any) error {
	raw, e := json.Marshal(fp)
	if e != nil {
		return e
	}
	tag, e := s.Pool.Exec(ctx, `INSERT INTO generation_entries(cache_key,statement_id,kind,request_fingerprint,status,owner,lease_until) VALUES($1,$2,$3,$4,'running',$5,clock_timestamp()+interval '90 seconds') ON CONFLICT(cache_key) DO UPDATE SET status='running',owner=$5,lease_until=clock_timestamp()+interval '90 seconds',updated_at=clock_timestamp() WHERE generation_entries.status='failed' OR (generation_entries.status IN ('pending','running') AND generation_entries.lease_until<clock_timestamp())`, key, sid, kind, raw, owner)
	if e == nil && tag.RowsAffected() != 1 {
		return domain.Fail(domain.Conflict)
	}
	return e
}
func (s *Store) GenerationHeartbeat(ctx context.Context, key, owner string) error {
	tag, e := s.Pool.Exec(ctx, "UPDATE generation_entries SET lease_until=clock_timestamp()+interval '90 seconds',updated_at=clock_timestamp() WHERE cache_key=$1 AND owner=$2 AND status='running' AND lease_until>clock_timestamp()", key, owner)
	if e == nil && tag.RowsAffected() != 1 {
		return domain.Fail(domain.Conflict)
	}
	return e
}
func (s *Store) EndGeneration(ctx context.Context, key, owner string, result json.RawMessage, err error) error {
	status := "ready"
	if err != nil {
		status = "failed"
		result = nil
	}
	tag, e := s.Pool.Exec(ctx, "UPDATE generation_entries SET status=$3,result=$4,owner=NULL,lease_until=NULL,updated_at=clock_timestamp() WHERE cache_key=$1 AND owner=$2 AND status='running' AND lease_until>clock_timestamp()", key, owner, status, result)
	if e == nil && tag.RowsAffected() != 1 {
		return domain.Fail(domain.Conflict)
	}
	return e
}
