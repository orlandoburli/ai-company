-- 0020_runners.sql — runners (the transport) and the agents' runner chains.
--
-- The agent registry splits in two:
--
--   * the agent is the WHAT — persona, role, skills, capabilities;
--   * the runner is the HOW — a CLI binary or an API model, with the
--     transport config the adapter needs.
--
-- An agent names an ordered chain of runners; the chain's priority is the
-- fallback order the runtime walks when a runner fails. That is the idea the
-- agents' `fallbacks_json` column used to carry (an ordered list for
-- capability failover), moved onto the transport itself.
--
-- The whole file is one transaction — the migration runner wraps every file
-- in a single transaction — so the conversion below and the new tables land
-- together. A half-applied migration cannot leave agents without runners.
--
-- The conversion reads every agent of every tenant, so this file must be
-- applied by the schema owner (the owner bypasses RLS; the runtime role must
-- not).

-- ---------------------------------------------------------------------------
-- runners: the transport.
--
-- name is mentionable-shaped (a lowercase slug), like the agents' names, so a
-- runner can be named in an operator's notes; UNIQUE (tenant_id, name) keeps
-- it unambiguous per tenant.
--
-- config_json is the transport config the adapter expects:
--   cli -> {bin, args, env, timeout_sec, output_format}
--   api -> {provider, model, base_url, key_env, max_tokens}
-- It is validated per kind by internal/runner before it is written.
--
-- credential_ref is a REFERENCE to a secret held outside the database (the
-- OS keychain), never the secret itself; the api kind's key_env names an
-- environment variable, never a key.
--
-- budget_tokens = 0 and budget_cost_usd = 0 mean "no budget recorded" (the
-- runtime treats them as unlimited).
-- ---------------------------------------------------------------------------
CREATE TABLE runners (
    id              TEXT PRIMARY KEY,
    tenant_id       TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name            TEXT        NOT NULL
                                CHECK (name ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    kind            TEXT        NOT NULL
                                CHECK (kind IN ('cli', 'api')),
    config_json     JSONB       NOT NULL DEFAULT '{}',
    credential_ref  TEXT        NOT NULL DEFAULT '',
    budget_tokens   BIGINT      NOT NULL DEFAULT 0
                                CHECK (budget_tokens >= 0),
    budget_cost_usd NUMERIC(12,6) NOT NULL DEFAULT 0
                                CHECK (budget_cost_usd >= 0),
    enabled         BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, name),
    UNIQUE (id, tenant_id)          -- target for composite foreign keys
);

CREATE INDEX runners_tenant_idx ON runners (tenant_id, name);

COMMENT ON TABLE runners IS
    'The transport of an agent run: a CLI binary or an API model, with its config.';
COMMENT ON COLUMN runners.credential_ref IS
    'Reference to a secret held outside the database; never contains the secret.';
COMMENT ON COLUMN runners.config_json IS
    'Transport config, interpreted per kind: cli -> {bin,args,env,timeout_sec,output_format}, api -> {provider,model,base_url,key_env,max_tokens}.';

-- ---------------------------------------------------------------------------
-- agent_runners: the ordered chain — the fallback order.
--
-- Both parents are linked with COMPOSITE foreign keys on (id, tenant_id), so a
-- chain can never point at another tenant's agent or runner: the database
-- refuses the reference regardless of what the application passes.
--
-- UNIQUE (tenant_id, agent_id, priority) makes the order unambiguous — one
-- runner per position — and UNIQUE (tenant_id, agent_id, runner_id) keeps a
-- runner from appearing twice in the same chain.
-- ---------------------------------------------------------------------------
CREATE TABLE agent_runners (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    agent_id   TEXT        NOT NULL,
    runner_id  TEXT        NOT NULL,
    priority   INT         NOT NULL DEFAULT 1 CHECK (priority > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, agent_id, runner_id),
    UNIQUE (tenant_id, agent_id, priority),

    FOREIGN KEY (agent_id, tenant_id)  REFERENCES agents (id, tenant_id)  ON DELETE CASCADE,
    FOREIGN KEY (runner_id, tenant_id) REFERENCES runners (id, tenant_id) ON DELETE CASCADE
);

CREATE INDEX agent_runners_agent_idx  ON agent_runners (tenant_id, agent_id, priority);
CREATE INDEX agent_runners_runner_idx ON agent_runners (tenant_id, runner_id);

COMMENT ON TABLE agent_runners IS
    'The ordered chain of runners an agent may use; priority is the fallback order.';

-- ---------------------------------------------------------------------------
-- agent_jobs: the runner snapshot.
--
-- A queued job must run the transport it was queued with, so the runner's id,
-- kind and config are copied onto the job row when the job is created. Editing
-- the runner afterwards never changes a job that is already queued, and the
-- job never has to read the agent to know how to run.
--
-- runner_kind is nullable: a job that has not picked a runner yet (or one
-- from before this migration) carries no snapshot.
-- ---------------------------------------------------------------------------
ALTER TABLE agent_jobs
    ADD COLUMN runner_id          TEXT,
    ADD COLUMN runner_kind        TEXT
                              CHECK (runner_kind IS NULL OR runner_kind IN ('cli', 'api')),
    ADD COLUMN runner_config_json JSONB NOT NULL DEFAULT '{}';

CREATE INDEX agent_jobs_runner_idx ON agent_jobs (tenant_id, runner_id);

-- ---------------------------------------------------------------------------
-- workflow_runs: the run's variables.
--
-- The run's inputs (the values a template's placeholders are filled with),
-- read by the engine when it assembles a node's task file.
-- ---------------------------------------------------------------------------
ALTER TABLE workflow_runs
    ADD COLUMN variables_json JSONB NOT NULL DEFAULT '{}';

-- ---------------------------------------------------------------------------
-- The conversion: every existing agent becomes one runner plus one binding.
--
-- Rules, exactly:
--   * one runner per agent, named after the agent (agent names are already
--     mentionable-shaped, so they are valid runner names);
--   * the runner carries that agent's kind and config_json, unchanged;
--   * if the tenant already has a runner with that name, that runner is
--     reused and its kind and config are updated to the agent's;
--   * the runner is bound to the agent with priority 1.
--
-- The agents' kind / config_json / fallbacks_json columns STAY for this
-- release: nothing reads them any more, and a later migration drops them
-- (together with the legacy Kind/Config fields on the Go type). Until then
-- they are the historical record of where each agent's transport came from.
--
-- The conversion is a function, so it is idempotent and can be re-run safely:
-- a second run finds the runners it created, reuses them and rewrites the
-- same bindings. Safe on an empty database too — the source query returns no
-- rows and nothing is written.
--
-- That also covers a tenant seeded AFTER this migration: its agents still
-- carry their legacy kind/config, so one more call gives them their runners
-- (the same call the seed's owners would make).
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.convert_agents_to_runners() RETURNS void
LANGUAGE sql
SET search_path = public, pg_temp
AS $$
    WITH converted AS (
        SELECT a.tenant_id, a.id AS agent_id, a.name, a.kind, a.config_json
          FROM agents a
    ),
    upserted AS (
        INSERT INTO runners (id, tenant_id, name, kind, config_json)
        SELECT upper(gen_random_uuid()::text), c.tenant_id, c.name, c.kind, c.config_json
          FROM converted c
        ON CONFLICT (tenant_id, name) DO UPDATE
            SET kind = EXCLUDED.kind,
                config_json = EXCLUDED.config_json,
                updated_at = now()
        RETURNING id, tenant_id, name
    )
    INSERT INTO agent_runners (id, tenant_id, agent_id, runner_id, priority)
    SELECT upper(gen_random_uuid()::text), u.tenant_id, c.agent_id, u.id, 1
      FROM converted c
      JOIN upserted u
        ON u.tenant_id = c.tenant_id AND u.name = c.name
    ON CONFLICT (tenant_id, agent_id, runner_id) DO NOTHING
$$;

SELECT public.convert_agents_to_runners();

-- The conversion is a migration step, not an API: the runtime role has no
-- business running it.
REVOKE EXECUTE ON FUNCTION public.convert_agents_to_runners() FROM PUBLIC;

-- ---------------------------------------------------------------------------
-- Row-level security: ENABLE + FORCE on both new tables, exactly like every
-- other tenant table in this schema. The app role owns nothing and lacks
-- BYPASSRLS, so a forgotten WHERE tenant_id is an empty result, never
-- another tenant's rows.
-- ---------------------------------------------------------------------------
ALTER TABLE runners       ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_runners ENABLE ROW LEVEL SECURITY;

ALTER TABLE runners       FORCE ROW LEVEL SECURITY;
ALTER TABLE agent_runners FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON runners
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation ON agent_runners
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());

-- ---------------------------------------------------------------------------
-- Privileges. 0001's default privileges would grant aic_app the usual DML on
-- every new table anyway; these are spelled out so a later change to the
-- defaults cannot silently change this file's contract.
-- ---------------------------------------------------------------------------
GRANT SELECT, INSERT, UPDATE, DELETE ON runners       TO aic_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON agent_runners TO aic_app;
