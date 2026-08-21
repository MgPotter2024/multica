-- Agent-level issue-policy role (ARG-548 M9/M10).
--
--   ''             — default: no extra capability (every existing and new agent)
--   'orchestrator' — may create / re-parent sub-issues, but only under an
--                    issue currently assigned to that same agent
--   'reviewer'     — may perform exactly the in_review -> done status
--                    transition, never on an issue assigned to itself
--
-- Writes are restricted to human workspace owners/admins in the UpdateAgent
-- handler; agent-authenticated paths are rejected there (privilege-escalation
-- boundary). Additive: NOT NULL is safe because a DEFAULT is provided.
ALTER TABLE agent ADD COLUMN role TEXT NOT NULL DEFAULT ''
    CHECK (role IN ('', 'orchestrator', 'reviewer'));
