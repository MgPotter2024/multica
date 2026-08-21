-- Revert 183: drop only the column this migration added (its inline CHECK
-- constraint is dropped with it).
ALTER TABLE agent DROP COLUMN role;
