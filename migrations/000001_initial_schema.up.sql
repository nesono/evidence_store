CREATE TYPE evidence_result AS ENUM ('PASS', 'FAIL', 'ERROR', 'SKIPPED');

CREATE TABLE evidence (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    repo           TEXT NOT NULL,
    finished_at    TIMESTAMPTZ NOT NULL,
    rcs_ref        TEXT NOT NULL,
    branch         TEXT NOT NULL,
    result         evidence_result NOT NULL,
    evidence_type  TEXT NOT NULL,
    procedure_ref  TEXT NOT NULL,
    source         TEXT NOT NULL,
    ingested_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    metadata       JSONB NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_evidence_repo           ON evidence (repo);
CREATE INDEX idx_evidence_rcs_ref        ON evidence (repo, rcs_ref);
CREATE INDEX idx_evidence_finished_at    ON evidence (finished_at);
CREATE INDEX idx_evidence_type           ON evidence (evidence_type);
CREATE INDEX idx_evidence_result         ON evidence (result);
CREATE INDEX idx_evidence_procedure_ref  ON evidence (procedure_ref);
CREATE INDEX idx_evidence_source         ON evidence (source);
CREATE INDEX idx_evidence_metadata       ON evidence USING GIN (metadata);
CREATE INDEX idx_evidence_cursor         ON evidence (ingested_at, id);

CREATE TABLE inheritance_declaration (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    repo            TEXT NOT NULL,
    source_rcs_ref  TEXT NOT NULL,
    target_rcs_ref  TEXT NOT NULL,
    scope           JSONB NOT NULL DEFAULT '[]',
    justification   TEXT NOT NULL,
    created_by      TEXT NOT NULL
);

CREATE INDEX idx_inheritance_target ON inheritance_declaration (repo, target_rcs_ref);
CREATE INDEX idx_inheritance_source ON inheritance_declaration (repo, source_rcs_ref);

CREATE TABLE retention_policy (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    evidence_type   TEXT,
    max_age_days    INT NOT NULL,
    keep_failures   BOOLEAN NOT NULL DEFAULT true,
    priority        INT NOT NULL DEFAULT 0
);
