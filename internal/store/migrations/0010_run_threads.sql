-- 0010_run_threads.sql — one chat thread per run.
--
-- A run has its own conversation: the checkpoint decisions, the steering
-- interventions and the system messages of the run all land there (FR-CP-4,
-- FR-MON-6). The item thread from M2 stays the item's conversation.
--
-- The partial unique index is what makes the thread get-or-create safe
-- without a lock: two concurrent requests for the same run produce one row.
-- The 0008 migration added the column and the foreign key; this index is the
-- missing half of that guarantee.

CREATE UNIQUE INDEX threads_run_idx
    ON threads (tenant_id, run_id)
    WHERE context_kind = 'run';
