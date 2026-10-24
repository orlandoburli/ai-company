-- 0023_agent_credential.sql — the identity an agent acts under.
--
-- credential_ref is a reference to a secret held outside the database
-- (<service>/<account>), never the secret itself. It belongs to the agent's
-- persona; a runner's credential_ref remains the fallback for standalone or
-- legacy runner configurations.
--
-- This adds a column to an existing tenant-scoped table, so the existing RLS
-- policy and grants apply; no RLS or grant changes are needed.

ALTER TABLE agents
    ADD COLUMN credential_ref TEXT NOT NULL DEFAULT '';

COMMENT ON COLUMN agents.credential_ref IS
    'Reference to the secret identity this agent acts under (<service>/<account>); never contains the secret.';
