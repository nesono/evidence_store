CREATE TABLE campaign_draft_blob_ref (
    attempt_id UUID NOT NULL REFERENCES campaign_attempt(id) ON DELETE CASCADE,
    digest TEXT NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    PRIMARY KEY (attempt_id, digest)
);
CREATE INDEX idx_campaign_draft_blob_ref_digest ON campaign_draft_blob_ref(digest);
