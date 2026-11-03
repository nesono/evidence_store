CREATE TABLE campaign_execution (
    id UUID PRIMARY KEY,
    package_id UUID NOT NULL REFERENCES campaign_package(id),
    name TEXT NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
    repo TEXT NOT NULL CHECK (length(btrim(repo)) > 0),
    branch TEXT NOT NULL CHECK (length(btrim(branch)) > 0),
    rcs_ref TEXT NOT NULL CHECK (length(btrim(rcs_ref)) > 0),
    created_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revision BIGINT NOT NULL DEFAULT 1,
    closed_at TIMESTAMPTZ,
    closed_by TEXT,
    UNIQUE (id, package_id)
);
CREATE INDEX campaign_execution_listing ON campaign_execution(updated_at DESC, id);
ALTER TABLE campaign_attempt ADD COLUMN execution_id UUID;
ALTER TABLE campaign_attempt ADD CONSTRAINT campaign_attempt_execution_package
    FOREIGN KEY (execution_id, campaign_id) REFERENCES campaign_execution(id, package_id);
ALTER TABLE campaign_attempt ADD COLUMN updated_by TEXT;
ALTER TABLE campaign_attempt ADD COLUMN finalized_by TEXT;
CREATE INDEX campaign_attempt_execution ON campaign_attempt(execution_id, procedure_ref, created_at DESC, id DESC);
CREATE UNIQUE INDEX campaign_attempt_one_shared_draft ON campaign_attempt(execution_id, procedure_ref)
    WHERE execution_id IS NOT NULL AND finalized_at IS NULL;
CREATE TABLE campaign_execution_event (
    execution_id UUID NOT NULL REFERENCES campaign_execution(id),
    revision BIGINT NOT NULL,
    action TEXT NOT NULL CHECK (action IN ('closed', 'reopened')),
    actor TEXT NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (execution_id, revision)
);
