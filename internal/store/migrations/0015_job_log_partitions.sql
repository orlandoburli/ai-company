-- 0015_job_log_partitions.sql — convert append-only job_logs to monthly ranges.
--
-- The conversion is transactional and takes an ACCESS EXCLUSIVE lock while the
-- existing rows are copied. That lock is required to avoid losing concurrent
-- appends; a shadow-table/dual-write rollout would be needed to avoid it.
--
-- 0001's blanket default privileges gave aic_app direct rights on new tables.
-- Existing and future partitions created by this migration owner are therefore
-- explicitly revoked, and future tables in public no longer receive that
-- blanket grant. Each partition also has ENABLE + FORCE RLS and its own policy,
-- so even a future grant cannot bypass tenant isolation by addressing a child
-- directly. Partition RLS adds a second guard; parent queries are governed by
-- the parent's policy, while direct child queries use the child's policy.

LOCK TABLE public.job_logs IN ACCESS EXCLUSIVE MODE;

ALTER TABLE public.job_logs RENAME TO job_logs_before_partitioning;

CREATE TABLE public.job_logs_new (
    seq         BIGINT GENERATED ALWAYS AS IDENTITY,
    tenant_id   TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    job_id      TEXT        NOT NULL,
    stream      TEXT        CHECK (stream IN ('stdout', 'stderr', 'engine', 'adapter')),
    level       TEXT        CHECK (level IN ('debug', 'info', 'warn', 'error')),
    kind        TEXT        CHECK (kind IN ('output_chunk', 'tool_call', 'tool_result',
                                              'usage', 'lifecycle', 'error')),
    message     TEXT        NOT NULL DEFAULT '',
    data_json   JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    run_id      TEXT,
    step_run_id TEXT,

    -- PostgreSQL requires every unique constraint on a partitioned table to
    -- include created_at. seq remains identity-generated but is no longer
    -- globally unique by a database constraint; the composite key is the
    -- closest legal primary key and rows remain individually addressable.
    CONSTRAINT job_logs_new_pkey PRIMARY KEY (seq, created_at),
    -- (job_id, seq) cannot remain globally unique because it omits the range
    -- key. This legal shape enforces uniqueness within each partition key; the
    -- identity-generated seq itself remains practically monotonic but is not a
    -- global uniqueness constraint across partitions.
    CONSTRAINT job_logs_new_job_seq_key UNIQUE (job_id, seq, created_at),
    CONSTRAINT job_logs_job_fk FOREIGN KEY (job_id, tenant_id)
        REFERENCES agent_jobs (id, tenant_id) ON DELETE CASCADE,
    CONSTRAINT job_logs_run_fk FOREIGN KEY (run_id, tenant_id)
        REFERENCES workflow_runs (id, tenant_id) ON DELETE SET NULL (run_id),
    CONSTRAINT job_logs_step_run_fk FOREIGN KEY (step_run_id, tenant_id)
        REFERENCES step_runs (id, tenant_id) ON DELETE SET NULL (step_run_id)
) PARTITION BY RANGE (created_at);

-- Create a contiguous monthly range covering old data, this month, and next
-- month. The default partition accepts any out-of-range writes.
DO $$
DECLARE
    oldest       TIMESTAMPTZ;
    newest       TIMESTAMPTZ;
    first_month  TIMESTAMP;
    last_month   TIMESTAMP;
    month_start  TIMESTAMP;
    month_end    TIMESTAMP;
    part_name    TEXT;
    lower_bound  TEXT;
    upper_bound  TEXT;
BEGIN
    SELECT min(created_at), max(created_at)
      INTO oldest, newest
      FROM public.job_logs_before_partitioning;

    -- Keep the loop bounds as UTC wall-clock timestamps. Month arithmetic on
    -- timestamptz otherwise follows the server session timezone and can repeat
    -- a month at UTC offset boundaries.
    first_month := date_trunc('month', now() AT TIME ZONE 'UTC');
    last_month := first_month + interval '1 month';
    IF oldest IS NOT NULL THEN
        first_month := LEAST(first_month,
            date_trunc('month', oldest AT TIME ZONE 'UTC'));
        last_month := GREATEST(last_month,
            date_trunc('month', newest AT TIME ZONE 'UTC'));
    END IF;

    month_start := first_month;
    WHILE month_start <= last_month LOOP
        month_end := month_start + interval '1 month';
        -- Use temporary child names because the old heap's partitions still
        -- occupy the final names until it is dropped below.
        part_name := 'job_logs_new_' || to_char(month_start, 'YYYY_MM');
        lower_bound := to_char(month_start, 'YYYY-MM-DD HH24:MI:SS') || '+00';
        upper_bound := to_char(month_end, 'YYYY-MM-DD HH24:MI:SS') || '+00';
        EXECUTE format(
            'CREATE TABLE public.%I PARTITION OF public.job_logs_new '
            'FOR VALUES FROM (%L::timestamptz) TO (%L::timestamptz)',
            part_name, lower_bound, upper_bound);
        month_start := month_end;
    END LOOP;

    CREATE TABLE public.job_logs_new_default PARTITION OF public.job_logs_new DEFAULT;
END
$$;

-- Preserve the identity values while copying. The sequence is advanced below
-- so the next generated value cannot collide with copied rows.
INSERT INTO public.job_logs_new
    (seq, tenant_id, job_id, stream, level, kind, message, data_json,
     created_at, run_id, step_run_id)
OVERRIDING SYSTEM VALUE
SELECT seq, tenant_id, job_id, stream, level, kind, message, data_json,
       created_at, run_id, step_run_id
  FROM public.job_logs_before_partitioning;

SELECT setval(
    pg_get_serial_sequence('public.job_logs_new', 'seq'),
    COALESCE((SELECT max(seq) FROM public.job_logs_new), 1),
    EXISTS (SELECT 1 FROM public.job_logs_new)
);

-- Secure existing partitions explicitly. Child RLS remains a defense in depth
-- measure, while the application receives access only to the parent relation.
DO $$
DECLARE
    part RECORD;
BEGIN
    FOR part IN
        SELECT child.oid::regclass AS relation
          FROM pg_inherits i
          JOIN pg_class child ON child.oid = i.inhrelid
         WHERE i.inhparent = 'public.job_logs_new'::regclass
    LOOP
        EXECUTE format('REVOKE ALL ON TABLE %s FROM aic_app', part.relation);
        EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', part.relation);
        EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', part.relation);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %s USING (tenant_id = current_tenant_id()) '
            'WITH CHECK (tenant_id = current_tenant_id())', part.relation);
    END LOOP;
END
$$;

-- Drop the old heap only after all rows have landed in the replacement.
DROP TABLE public.job_logs_before_partitioning;
ALTER TABLE public.job_logs_new RENAME TO job_logs;

DO $$
DECLARE
    part RECORD;
    final_name TEXT;
BEGIN
    FOR part IN
        SELECT child.oid::regclass AS relation, child.relname
          FROM pg_inherits i
          JOIN pg_class child ON child.oid = i.inhrelid
         WHERE i.inhparent = 'public.job_logs'::regclass
    LOOP
        IF part.relname = 'job_logs_new_default' THEN
            final_name := 'job_logs_default';
        ELSE
            final_name := replace(part.relname, 'job_logs_new_', 'job_logs_');
        END IF;
        EXECUTE format('ALTER TABLE %s RENAME TO %I', part.relation, final_name);
    END LOOP;
END
$$;

-- Reattach after the parent has its permanent name. DETACH/ATTACH is done in
-- this same transaction, so no other session can observe the intermediate gap.
DO $$
DECLARE
    part RECORD;
BEGIN
    FOR part IN
        SELECT child.oid::regclass AS relation,
               pg_get_expr(child.relpartbound, child.oid) AS bound
          FROM pg_inherits i
          JOIN pg_class child ON child.oid = i.inhrelid
         WHERE i.inhparent = 'public.job_logs'::regclass
         ORDER BY child.relname
    LOOP
        EXECUTE format('ALTER TABLE public.job_logs DETACH PARTITION %s', part.relation);
        IF part.bound = 'DEFAULT' THEN
            EXECUTE format('ALTER TABLE public.job_logs ATTACH PARTITION %s DEFAULT', part.relation);
        ELSE
            -- The deparsed bound is PostgreSQL-generated catalog text, not user input.
            EXECUTE format('ALTER TABLE public.job_logs ATTACH PARTITION %s %s', part.relation, part.bound);
        END IF;
    END LOOP;
END
$$;

ALTER INDEX public.job_logs_new_pkey RENAME TO job_logs_pkey;
ALTER TABLE public.job_logs RENAME CONSTRAINT job_logs_new_job_seq_key TO job_logs_job_seq_key;
CREATE INDEX job_logs_job_idx ON public.job_logs (job_id, seq);
CREATE INDEX job_logs_tenant_idx ON public.job_logs (tenant_id, created_at);
CREATE INDEX job_logs_run_idx ON public.job_logs (run_id, seq);

CREATE TRIGGER job_logs_no_update
    BEFORE UPDATE OR DELETE ON public.job_logs
    FOR EACH ROW EXECUTE FUNCTION public.job_logs_is_append_only();

ALTER TABLE public.job_logs ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.job_logs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON public.job_logs
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT ON public.job_logs TO aic_app;
REVOKE UPDATE, DELETE ON public.job_logs FROM aic_app;

-- Remove 0001's schema-scoped default table grant for the migration owner.
-- Subsequent migrations must grant explicitly; partitions created by this owner
-- in public later will not receive aic_app access directly.
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLES FROM aic_app;
