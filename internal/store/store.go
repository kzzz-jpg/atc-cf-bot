package store

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/kzzz/atc-cf-bot/internal/config"
	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/migrations"
	"github.com/pressly/goose/v3"
)

const SchemaVersion int64 = 2

type Store struct {
	Pool         *pgxpool.Pool
	Timeout      time.Duration
	LeaseSeconds int
}

func Open(ctx context.Context, c config.Config) (*Store, error) {
	cfg, e := pgxpool.ParseConfig(c.Database.URL)
	if e != nil {
		return nil, domain.Fail(domain.Configuration)
	}
	cfg.MaxConns = int32(c.Database.MaxConnections)
	cfg.ConnConfig.ConnectTimeout = time.Duration(c.Database.QueryTimeout) * time.Second
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = fmt.Sprint(c.Database.QueryTimeout * 1000)
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		return nil, domain.Fail(domain.Configuration)
	}
	if e = pool.Ping(ctx); e != nil {
		pool.Close()
		return nil, domain.Fail(domain.Transient)
	}
	return &Store{Pool: pool, Timeout: time.Duration(c.Database.QueryTimeout) * time.Second, LeaseSeconds: c.Worker.Lease}, nil
}
func (s *Store) Close() { s.Pool.Close() }
func (s *Store) transaction(ctx context.Context, fn func(pgx.Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	tx, e := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if e != nil {
		return e
	}
	defer func() {
		clean, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = tx.Rollback(clean)
	}()
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) Now(ctx context.Context) (time.Time, error) {
	var t time.Time
	e := s.Pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&t)
	return t, e
}
func (s *Store) CheckSchema(ctx context.Context) error {
	var v int64
	var applied bool
	e := s.Pool.QueryRow(ctx, "SELECT version_id,is_applied FROM goose_db_version ORDER BY id DESC LIMIT 1").Scan(&v, &applied)
	if e != nil || !applied || v != SchemaVersion {
		return domain.Fail(domain.Configuration)
	}
	tables := []string{"guild_configs", "guild_runtime", "catalog_snapshots", "catalog_heads", "problems", "catalog_entries", "statement_versions", "statement_assets", "statement_asset_links", "source_rejections", "generation_entries", "daily_publications", "problem_publications", "problem_reservations", "posting_jobs", "outbound_operations", "reconciliation_decisions", "interaction_receipts", "goose_db_version"}
	var n int
	e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name=ANY($1)", tables).Scan(&n)
	if e != nil || n != len(tables) {
		return domain.Fail(domain.Configuration)
	}
	required := map[string][]string{"guild_configs": {"guild_id", "version", "settings", "pending_schedule", "pending_effective_at"}, "guild_runtime": {"guild_id", "resume_job_id", "lease_owner", "lease_until", "lease_epoch", "block_reason"}, "posting_jobs": {"id", "guild_id", "daily_id", "publication_id", "deadline_at", "dedup_key", "lease_epoch", "auto_retry_count"}, "problem_publications": {"id", "guild_id", "daily_id", "problem_id", "statement_id", "generation_key", "candidate_attempts", "attempted_problem_ids", "first_published_at", "completed_at"}, "outbound_operations": {"id", "daily_id", "publication_id", "payload", "payload_hash", "expected_before_hash", "renderer_version", "discord_id", "result", "send_started_at"}, "statement_versions": {"id", "problem_id", "ast", "content_hash", "parser_version"}, "daily_publications": {"id", "guild_id", "local_date", "ends_at", "config_snapshot", "target_count", "thread_id", "starter_message_id"}}
	var tableNames, columnNames []string
	for table, columns := range required {
		for _, column := range columns {
			tableNames = append(tableNames, table)
			columnNames = append(columnNames, column)
		}
	}
	e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM unnest($1::text[],$2::text[]) AS expected(tbl,col) JOIN information_schema.columns c ON c.table_schema=current_schema() AND c.table_name=expected.tbl AND c.column_name=expected.col`, tableNames, columnNames).Scan(&n)
	if e != nil || n != len(columnNames) {
		return domain.Fail(domain.Configuration)
	}
	e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace ns ON ns.oid=c.relnamespace WHERE ns.nspname=current_schema() AND t.tgname=ANY($1) AND t.tgenabled='O'`, []string{"immutable_statement", "immutable_ready", "immutable_payload"}).Scan(&n)
	if e != nil || n != 3 {
		return domain.Fail(domain.Configuration)
	}
	e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint c JOIN pg_namespace ns ON ns.oid=c.connamespace WHERE ns.nspname=current_schema() AND c.contype='f' AND array_length(c.conkey,1)>=2 AND c.convalidated`).Scan(&n)
	if e != nil || n < 11 {
		return domain.Fail(domain.Configuration)
	}

	return nil
}
func Migrate(ctx context.Context, dsn string) error {
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		return domain.Fail(domain.Configuration)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	p, e := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if e != nil {
		return e
	}
	_, e = p.Up(ctx)
	return e
}

type Singleton struct {
	conn *pgx.Conn
	key  int64
}

func AcquireSingleton(ctx context.Context, dsn string) (*Singleton, error) {
	c, e := pgx.Connect(ctx, dsn)
	if e != nil {
		return nil, domain.Fail(domain.Transient)
	}
	var db, schema string
	e = c.QueryRow(ctx, "SELECT current_database(),current_schema()").Scan(&db, &schema)
	if e != nil {
		_ = c.Close(ctx)
		return nil, e
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte("atc-cf-bot:singleton:" + db + ":" + schema))
	key := int64(h.Sum64())
	var ok bool
	e = c.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&ok)
	if e != nil || !ok {
		_ = c.Close(ctx)
		return nil, domain.Fail(domain.Conflict)
	}
	return &Singleton{conn: c, key: key}, nil
}
func (s *Singleton) Monitor(ctx context.Context, cancel context.CancelFunc) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			probe, c := context.WithTimeout(ctx, 3*time.Second)
			var ok bool
			e := s.conn.QueryRow(probe, "SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND pid=pg_backend_pid() AND granted)").Scan(&ok)
			c()
			if e != nil || !ok {
				cancel()
				return
			}
		}
	}
}
func (s *Singleton) Close() {
	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	_ = s.conn.Close(ctx)
}
