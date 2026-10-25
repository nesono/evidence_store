CREATE TABLE campaign_attempt (
    id UUID PRIMARY KEY,
    campaign_id UUID NOT NULL REFERENCES campaign_package(id),
    procedure_ref TEXT NOT NULL,
    owner TEXT NOT NULL,
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    data JSONB NOT NULL CHECK (jsonb_typeof(data) = 'object'),
    last_operation_id UUID,
    last_base_revision BIGINT
);
CREATE INDEX idx_campaign_attempt_owner ON campaign_attempt(owner, campaign_id, procedure_ref, created_at, id);
