CREATE TABLE retention_policy (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    evidence_type   TEXT,
    max_age_days    INT NOT NULL,
    keep_failures   BOOLEAN NOT NULL DEFAULT true,
    priority        INT NOT NULL DEFAULT 0
);
