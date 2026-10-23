-- 0022_artifact_kinds.sql — expand the vocabulary of a run's evidence.
-- The engine writes branch, commit, diff_summary, file, report, issue_body,
-- item, and pr artifacts; these kinds are the vocabulary of the run's evidence.
ALTER TABLE public.artifacts DROP CONSTRAINT artifacts_kind_check;
ALTER TABLE public.artifacts
    ADD CONSTRAINT artifacts_kind_check
    CHECK (kind IN ('branch', 'commit', 'pr', 'diff_summary', 'file', 'report',
                    'issue_body', 'item', 'issue', 'document', 'wiki_page', 'link'));
