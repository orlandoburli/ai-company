-- 0011_checkpoint_fields.sql — the structured fields a checkpoint asks for.
--
-- A checkpoint may declare typed fields (string/text/boolean/number/choice)
-- that the human fills in on approval; the values enter the workflow memory
-- and select the outgoing edge at a branch node (FR-CP-7).
--
-- The node's config carries the spec, but the checkpoint row is what the
-- inbox renders and what survives the run, so the spec is copied into the
-- row when the checkpoint is created. The values the human submits live in
-- the existing `fields_json` column.

ALTER TABLE checkpoints ADD COLUMN fields_spec_json JSONB;
