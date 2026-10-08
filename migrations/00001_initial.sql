-- +goose Up
CREATE TABLE guild_configs (
 guild_id text PRIMARY KEY CHECK (guild_id ~ '^[1-9][0-9]{5,19}$'), version bigint NOT NULL CHECK(version>0), settings jsonb NOT NULL,
 pending_schedule jsonb, pending_effective_at timestamptz, updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK ((pending_schedule IS NULL)=(pending_effective_at IS NULL))
);
CREATE TABLE guild_runtime (
 guild_id text PRIMARY KEY REFERENCES guild_configs, next_due_at timestamptz, cycle_end_at timestamptz, active_daily_id uuid,
 block_reason text, resume_job_id uuid, lease_owner uuid, lease_until timestamptz, lease_epoch bigint NOT NULL DEFAULT 0 CHECK(lease_epoch>=0),
 CHECK((lease_owner IS NULL)=(lease_until IS NULL))
);
CREATE TABLE catalog_snapshots (
 id uuid PRIMARY KEY, platform text NOT NULL CHECK(platform IN ('atcoder','codeforces')), status text NOT NULL CHECK(status IN ('building','ready','failed')),
 fetched_at timestamptz NOT NULL DEFAULT clock_timestamp(), source_hash text NOT NULL, parser_version text NOT NULL, UNIQUE(id,platform)
);
CREATE TABLE catalog_heads (
 platform text PRIMARY KEY CHECK(platform IN ('atcoder','codeforces')), snapshot_id uuid NOT NULL, updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(snapshot_id,platform) REFERENCES catalog_snapshots(id,platform)
);
CREATE TABLE problems (
 id uuid PRIMARY KEY, platform text NOT NULL CHECK(platform IN ('atcoder','codeforces')), external_id text NOT NULL, canonical_url text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(), UNIQUE(platform,external_id), UNIQUE(id,platform)
);
CREATE TABLE catalog_entries (
 snapshot_id uuid NOT NULL, problem_id uuid NOT NULL, platform text NOT NULL, contest_id text NOT NULL, difficulty integer,
 contest_end_at timestamptz, contest_finished boolean NOT NULL, kind text NOT NULL CHECK(kind IN ('programming','unsupported','unknown')),
 metadata jsonb NOT NULL, PRIMARY KEY(snapshot_id,problem_id),
 FOREIGN KEY(snapshot_id,platform) REFERENCES catalog_snapshots(id,platform), FOREIGN KEY(problem_id,platform) REFERENCES problems(id,platform)
);
CREATE TABLE statement_versions (
 id uuid PRIMARY KEY, problem_id uuid NOT NULL REFERENCES problems, language text NOT NULL, content_hash text NOT NULL, parser_version text NOT NULL,
 ast jsonb NOT NULL, raw_html text NOT NULL, fetched_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(problem_id,content_hash,parser_version,language), UNIQUE(id,problem_id)
);
CREATE TABLE statement_assets (
 id uuid PRIMARY KEY, sha256 text NOT NULL UNIQUE, mime_type text NOT NULL, byte_size bigint NOT NULL CHECK(byte_size>0), body bytea NOT NULL,
 source_url text NOT NULL, CHECK(octet_length(body)=byte_size)
);
CREATE TABLE statement_asset_links (statement_id uuid NOT NULL REFERENCES statement_versions, asset_id uuid NOT NULL REFERENCES statement_assets, PRIMARY KEY(statement_id,asset_id));
CREATE TABLE source_rejections (
 problem_id uuid NOT NULL REFERENCES problems, parser_version text NOT NULL, reason_code text NOT NULL, checked_at timestamptz NOT NULL DEFAULT clock_timestamp(), recheck_after timestamptz,
 PRIMARY KEY(problem_id,parser_version)
);
CREATE TABLE generation_entries (
 cache_key text PRIMARY KEY CHECK(cache_key ~ '^[0-9a-f]{64}$'), statement_id uuid NOT NULL REFERENCES statement_versions,
 kind text NOT NULL CHECK(kind IN ('translation','hint','solution')), request_fingerprint jsonb NOT NULL,
 status text NOT NULL CHECK(status IN ('pending','running','ready','failed')), owner uuid, lease_until timestamptz,
 result jsonb, usage jsonb, updated_at timestamptz NOT NULL DEFAULT clock_timestamp(), UNIQUE(cache_key,statement_id),
 CHECK(status<>'ready' OR (result IS NOT NULL AND result<>'null'::jsonb AND result<>'{}'::jsonb))
);
CREATE TABLE daily_publications (
 id uuid PRIMARY KEY, guild_id text NOT NULL REFERENCES guild_configs, local_date date NOT NULL, starts_at timestamptz NOT NULL, ends_at timestamptz NOT NULL,
 timezone text NOT NULL, forum_channel_id text NOT NULL, config_snapshot jsonb NOT NULL, target_count integer NOT NULL CHECK(target_count BETWEEN 1 AND 10),
 thread_id text, starter_message_id text, status text NOT NULL CHECK(status IN ('pending','preparing','publishing','partial','completed','failed','needs_reconciliation','blocked','expired')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(), UNIQUE(guild_id,local_date), UNIQUE(id,guild_id), CHECK(ends_at>starts_at)
);
CREATE TABLE problem_publications (
 id uuid PRIMARY KEY, daily_id uuid NOT NULL, guild_id text NOT NULL, kind text NOT NULL CHECK(kind IN ('daily','manual')), slot_no integer NOT NULL CHECK(slot_no>0),
 problem_id uuid REFERENCES problems, statement_id uuid, generation_key text, source text NOT NULL CHECK(source IN ('atcoder','codeforces')), catalog_snapshot_id uuid REFERENCES catalog_snapshots,
 min_difficulty integer NOT NULL, max_difficulty integer NOT NULL, candidate_attempts integer NOT NULL DEFAULT 0 CHECK(candidate_attempts BETWEEN 0 AND 5),
 status text NOT NULL CHECK(status IN ('empty','reserved','preparing','ready','publishing','completed','failed','expired')), first_published_at timestamptz, completed_at timestamptz,
 FOREIGN KEY(daily_id,guild_id) REFERENCES daily_publications(id,guild_id), FOREIGN KEY(statement_id,problem_id) REFERENCES statement_versions(id,problem_id),
 FOREIGN KEY(generation_key,statement_id) REFERENCES generation_entries(cache_key,statement_id),
 UNIQUE(daily_id,kind,slot_no), UNIQUE(id,daily_id), UNIQUE(id,daily_id,guild_id), UNIQUE(id,guild_id,problem_id),
 CHECK(max_difficulty>=min_difficulty), CHECK(statement_id IS NULL OR problem_id IS NOT NULL), CHECK(generation_key IS NULL OR statement_id IS NOT NULL),
 CHECK(status<>'completed' OR (completed_at IS NOT NULL AND first_published_at IS NOT NULL AND generation_key IS NOT NULL))
);
CREATE UNIQUE INDEX daily_problem_unique ON problem_publications(daily_id,problem_id) WHERE problem_id IS NOT NULL;
CREATE TABLE problem_reservations (
 guild_id text NOT NULL, problem_id uuid NOT NULL, publication_id uuid NOT NULL, reserved_at timestamptz NOT NULL DEFAULT clock_timestamp(), PRIMARY KEY(guild_id,problem_id),
 FOREIGN KEY(publication_id,guild_id,problem_id) REFERENCES problem_publications(id,guild_id,problem_id)
);
CREATE TABLE posting_jobs (
 id uuid PRIMARY KEY, guild_id text NOT NULL, daily_id uuid NOT NULL, publication_id uuid, requested_by text, interaction_id text,
 dedup_key text NOT NULL UNIQUE, kind text NOT NULL CHECK(kind IN ('daily','manual')),
 status text NOT NULL CHECK(status IN ('queued','running','retry_wait','succeeded','failed','blocked','needs_reconciliation','expired')),
 available_at timestamptz NOT NULL DEFAULT clock_timestamp(), deadline_at timestamptz NOT NULL, attempts integer NOT NULL DEFAULT 0, auto_retry_count integer NOT NULL DEFAULT 0 CHECK(auto_retry_count BETWEEN 0 AND 2),
 lease_owner uuid, lease_until timestamptz, lease_epoch bigint NOT NULL DEFAULT 0, last_error_code text, retryable boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(daily_id,guild_id) REFERENCES daily_publications(id,guild_id), FOREIGN KEY(publication_id,daily_id,guild_id) REFERENCES problem_publications(id,daily_id,guild_id),
 UNIQUE(id,guild_id), CHECK((kind='manual')=(publication_id IS NOT NULL))
);
ALTER TABLE guild_runtime ADD FOREIGN KEY(active_daily_id,guild_id) REFERENCES daily_publications(id,guild_id);
ALTER TABLE guild_runtime ADD FOREIGN KEY(resume_job_id,guild_id) REFERENCES posting_jobs(id,guild_id);
CREATE TABLE outbound_operations (
 id uuid PRIMARY KEY, daily_id uuid NOT NULL REFERENCES daily_publications, publication_id uuid, sequence_no integer NOT NULL CHECK(sequence_no>0),
 kind text NOT NULL CHECK(kind IN ('forum','message','overview')), payload jsonb NOT NULL, payload_hash text NOT NULL, renderer_version text NOT NULL, expected_before_hash text,
 marker text NOT NULL UNIQUE, status text NOT NULL CHECK(status IN ('prepared','sending','confirmed','ambiguous','needs_reconciliation','failed')),
 discord_id text, result jsonb, send_started_at timestamptz, last_error_code text,
 FOREIGN KEY(publication_id,daily_id) REFERENCES problem_publications(id,daily_id), UNIQUE(daily_id,sequence_no),
 CHECK(status<>'confirmed' OR (discord_id IS NOT NULL AND discord_id ~ '^[1-9][0-9]{5,19}$' AND result IS NOT NULL AND result<>'null'::jsonb))
);
CREATE TABLE reconciliation_decisions (
 id uuid PRIMARY KEY, operation_id uuid NOT NULL REFERENCES outbound_operations, actor_label text NOT NULL, decision text NOT NULL CHECK(decision IN ('adopt','not_delivered')),
 reason text NOT NULL CHECK(length(reason)>0), adopted_discord_id text, created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE interaction_receipts (
 interaction_id text PRIMARY KEY, guild_id text NOT NULL REFERENCES guild_configs, kind text NOT NULL, posting_job_id uuid,
 accepted_at timestamptz NOT NULL DEFAULT clock_timestamp(), expires_at timestamptz NOT NULL DEFAULT clock_timestamp()+interval '24 hours',
 FOREIGN KEY(posting_job_id,guild_id) REFERENCES posting_jobs(id,guild_id)
);
CREATE INDEX catalog_difficulty ON catalog_entries(snapshot_id,difficulty,problem_id);
CREATE INDEX publication_history ON problem_publications(guild_id,first_published_at DESC,problem_id) WHERE first_published_at IS NOT NULL;
CREATE INDEX jobs_queue ON posting_jobs(available_at,id) WHERE status IN ('queued','retry_wait');
CREATE INDEX jobs_lease ON posting_jobs(lease_until) WHERE status='running';
CREATE INDEX daily_history ON daily_publications(guild_id,local_date DESC);
CREATE INDEX receipts_expiry ON interaction_receipts(expires_at);
-- +goose StatementBegin
CREATE FUNCTION enforce_immutable_content() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='statement_versions' THEN RAISE EXCEPTION 'statement versions are immutable'; END IF;
 IF TG_TABLE_NAME='generation_entries' AND OLD.status='ready' THEN RAISE EXCEPTION 'ready generation is immutable'; END IF;
 IF TG_TABLE_NAME='outbound_operations' THEN
  IF NEW.payload IS DISTINCT FROM OLD.payload OR NEW.payload_hash IS DISTINCT FROM OLD.payload_hash OR NEW.renderer_version IS DISTINCT FROM OLD.renderer_version THEN RAISE EXCEPTION 'outbound payload is immutable'; END IF;
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER immutable_statement BEFORE UPDATE ON statement_versions FOR EACH ROW EXECUTE FUNCTION enforce_immutable_content();
CREATE TRIGGER immutable_ready BEFORE UPDATE ON generation_entries FOR EACH ROW EXECUTE FUNCTION enforce_immutable_content();
CREATE TRIGGER immutable_payload BEFORE UPDATE ON outbound_operations FOR EACH ROW EXECUTE FUNCTION enforce_immutable_content();

-- +goose Down
DROP TABLE interaction_receipts, reconciliation_decisions, outbound_operations;
ALTER TABLE guild_runtime DROP CONSTRAINT guild_runtime_resume_job_id_guild_id_fkey;
ALTER TABLE guild_runtime DROP CONSTRAINT guild_runtime_active_daily_id_guild_id_fkey;
DROP TABLE posting_jobs, problem_reservations, problem_publications, daily_publications, generation_entries, source_rejections, statement_asset_links, statement_assets, statement_versions, catalog_entries, problems, catalog_heads, catalog_snapshots, guild_runtime, guild_configs;
DROP FUNCTION enforce_immutable_content();
