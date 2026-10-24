-- 0026_flow_endpoints.sql — a workflow graph may name where the flow starts
-- and where it ends.
--
-- The step row of a start or an end node is not work: the start hands the
-- flow to its successor, the end finishes it. The engine still records an
-- execution for both, so the step_runs.kind check has to know the two new
-- kinds — otherwise the engine could not write the row of a flow's first
-- and last node.
--
-- This only widens an existing check on a tenant-scoped table: the existing
-- RLS policy and grants apply; no RLS or grant changes are needed.

ALTER TABLE step_runs DROP CONSTRAINT step_runs_kind_check;

ALTER TABLE step_runs ADD CONSTRAINT step_runs_kind_check
    CHECK (kind IN ('agent', 'integration', 'checkpoint', 'loop', 'merge',
                     'start', 'end'));

COMMENT ON COLUMN step_runs.kind IS
    'The kind of the node this execution belongs to: agent, integration, checkpoint, loop, merge, start or end.';
