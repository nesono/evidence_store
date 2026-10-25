CREATE TABLE campaign_package (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    digest TEXT NOT NULL UNIQUE CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    archive_size BIGINT NOT NULL CHECK (archive_size > 0),
    imported_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    imported_by TEXT NOT NULL,
    definition JSONB NOT NULL CHECK (jsonb_typeof(definition) = 'object')
);

CREATE INDEX idx_campaign_package_imported ON campaign_package (imported_at, id);

-- Both archive and setup assets are roots, even before any evidence exists.
CREATE TABLE campaign_blob_ref (
    campaign_id UUID NOT NULL REFERENCES campaign_package(id) ON DELETE CASCADE,
    digest TEXT NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    PRIMARY KEY (campaign_id, digest)
);

CREATE INDEX idx_campaign_blob_ref_digest ON campaign_blob_ref (digest);
