CREATE TABLE issue_delivery_verification (
    task_id UUID PRIMARY KEY REFERENCES agent_task_queue(id) ON DELETE CASCADE,
    issue_id UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    agent_id UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    comment_id UUID NOT NULL REFERENCES comment(id) ON DELETE CASCADE,
    receipt JSONB NOT NULL CHECK (jsonb_typeof(receipt) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (issue_id, task_id)
);

CREATE INDEX idx_issue_delivery_verification_issue
    ON issue_delivery_verification(workspace_id, issue_id, created_at DESC);
