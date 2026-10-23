-- 0013_queue.sql — durable failure state and ranked dispatch order.

CREATE TABLE item_failures (
    id                       TEXT PRIMARY KEY,
    tenant_id                TEXT NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    item_id                  TEXT NOT NULL,
    workflow_id              TEXT NOT NULL,
    consecutive_failures     INT NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    state                    TEXT NOT NULL DEFAULT 'ok' CHECK (state IN ('ok', 'backing_off', 'exhausted')),
    curve                    TEXT NOT NULL CHECK (curve IN ('exponential', 'linear')),
    backoff_params_json      JSONB NOT NULL,
    backoff_version          TEXT NOT NULL,
    last_failure_kind        TEXT CHECK (last_failure_kind IN ('dependency', 'needs_input', 'capability', 'transient')),
    last_error_md            TEXT NOT NULL DEFAULT '',
    last_failed_at           TIMESTAMPTZ,
    next_eligible_at         TIMESTAMPTZ,
    exhausted_at             TIMESTAMPTZ,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, item_id, workflow_id),
    UNIQUE (id, tenant_id),
    FOREIGN KEY (item_id, tenant_id) REFERENCES items (id, tenant_id) ON DELETE CASCADE,
    FOREIGN KEY (workflow_id, tenant_id) REFERENCES workflow_templates (id, tenant_id) ON DELETE RESTRICT
);
CREATE INDEX item_failures_tenant_item_idx ON item_failures (tenant_id, item_id);
CREATE INDEX item_failures_dispatch_idx ON item_failures (tenant_id, state, next_eligible_at);

CREATE TABLE priority_rankings (
    id                 TEXT PRIMARY KEY,
    tenant_id          TEXT NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    state              TEXT NOT NULL CHECK (state IN ('running', 'ready', 'failed', 'superseded')),
    weights_version    TEXT NOT NULL,
    llm_model          TEXT,
    degraded_reason    TEXT CHECK (degraded_reason IN ('llm_failed', 'invalid_output', 'over_budget')),
    input_fingerprint  TEXT NOT NULL,
    summary_md         TEXT NOT NULL DEFAULT '',
    roots_json         JSONB NOT NULL DEFAULT '[]'::jsonb,
    computed_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at         TIMESTAMPTZ NOT NULL,
    superseded_by      TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, tenant_id),
    FOREIGN KEY (superseded_by, tenant_id) REFERENCES priority_rankings (id, tenant_id) ON DELETE SET NULL (superseded_by)
);
CREATE UNIQUE INDEX priority_rankings_one_ready_per_tenant ON priority_rankings (tenant_id) WHERE state = 'ready';
CREATE INDEX priority_rankings_latest_idx ON priority_rankings (tenant_id, computed_at DESC);

CREATE TABLE priority_ranking_entries (
    id             TEXT PRIMARY KEY,
    tenant_id      TEXT NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    ranking_id     TEXT NOT NULL,
    item_id        TEXT NOT NULL,
    rank           INT NOT NULL CHECK (rank > 0),
    score          INT NOT NULL CHECK (score BETWEEN 0 AND 1000),
    reason_md      TEXT NOT NULL,
    base_score     INT NOT NULL,
    factors_json   JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (ranking_id, item_id),
    UNIQUE (ranking_id, rank),
    FOREIGN KEY (ranking_id, tenant_id) REFERENCES priority_rankings (id, tenant_id) ON DELETE CASCADE,
    FOREIGN KEY (item_id, tenant_id) REFERENCES items (id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX priority_ranking_entries_cover_idx ON priority_ranking_entries (ranking_id, rank, score);
CREATE INDEX priority_ranking_entries_tenant_item_idx ON priority_ranking_entries (tenant_id, item_id);

-- Queue dispatch indexes are declared here as well so this migration can be
-- applied to installations that predate the original content index.
CREATE INDEX IF NOT EXISTS items_tenant_pinned_idx ON items (tenant_id, pinned_at DESC) WHERE pinned_at IS NOT NULL;

ALTER TABLE item_failures ENABLE ROW LEVEL SECURITY;
ALTER TABLE item_failures FORCE ROW LEVEL SECURITY;
ALTER TABLE priority_rankings ENABLE ROW LEVEL SECURITY;
ALTER TABLE priority_rankings FORCE ROW LEVEL SECURITY;
ALTER TABLE priority_ranking_entries ENABLE ROW LEVEL SECURITY;
ALTER TABLE priority_ranking_entries FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON item_failures
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation ON priority_rankings
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation ON priority_ranking_entries
    USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON item_failures TO aic_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON priority_rankings TO aic_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON priority_ranking_entries TO aic_app;
