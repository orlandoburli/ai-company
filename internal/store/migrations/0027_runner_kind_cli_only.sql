-- 0027_runner_kind_cli_only.sql — new registry runners are CLI transports.
--
-- Keep existing API runner rows readable so already queued legacy jobs can
-- still dispatch through the API adapter. NOT VALID skips checking those old
-- rows while enforcing the CLI-only vocabulary for inserts and updates.
-- RLS policies and grants are intentionally unchanged.

ALTER TABLE runners DROP CONSTRAINT runners_kind_check;
ALTER TABLE runners
    ADD CONSTRAINT runners_kind_cli_check CHECK (kind = 'cli') NOT VALID;
