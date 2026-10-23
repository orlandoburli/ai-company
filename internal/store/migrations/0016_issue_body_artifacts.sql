-- 0016_issue_body_artifacts.sql — allow fetched external issue text to be
-- retained as an explicitly typed, provenance-bearing run artifact.
ALTER TABLE public.artifacts DROP CONSTRAINT artifacts_kind_check;
ALTER TABLE public.artifacts
    ADD CONSTRAINT artifacts_kind_check
    CHECK (kind IN ('branch', 'commit', 'pr', 'diff_summary', 'file', 'report', 'issue_body'));
