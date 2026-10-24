-- 0025_github_link_kind.sql — a link row may describe a pull request.
--
-- GitHub numbers issues and pull requests in one sequence, so
-- (tenant_id, repo, issue_number) already identifies either kind uniquely;
-- the kind column records which one a row describes. Every row that exists
-- today describes an issue, and the default says so.
--
-- A pull request also carries the branches it was opened from, which is what
-- makes it recognisable as work on an item. An issue has no branches, so
-- those columns are empty for it.
--
-- This adds columns to an existing tenant-scoped table, so the existing RLS
-- policy and grants apply; no RLS or grant changes are needed.

ALTER TABLE github_issue_links
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'issue';

ALTER TABLE github_issue_links
    ADD CONSTRAINT github_issue_links_kind_check
    CHECK (kind IN ('issue', 'pr'));

ALTER TABLE github_issue_links
    ADD COLUMN head_branch TEXT NOT NULL DEFAULT '',
    ADD COLUMN base_branch TEXT NOT NULL DEFAULT '';

COMMENT ON COLUMN github_issue_links.kind IS
    'What the row links: a GitHub issue or a pull request.';
COMMENT ON COLUMN github_issue_links.head_branch IS
    'Head branch of a linked pull request; empty for an issue.';
COMMENT ON COLUMN github_issue_links.base_branch IS
    'Base branch of a linked pull request; empty for an issue.';
