-- Identities the server can name, and the roles they hold.
--
-- Until now a caller was a shared secret in an environment variable: no name,
-- no owner, no expiry, and no revocation short of a redeploy. These two tables
-- are what let a key be issued to someone, granted a role, and taken away
-- again -- and they are where an SSO login lands a human in phase 5, since a
-- principal is a principal whether it arrived with a bearer token or an ID
-- token. See docs/rbac-design.md section 4.

CREATE TABLE principals (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- "ci:nightly" for a robot, "user:alice@example.com" for a human. The
    -- prefix is convention, not constraint: what the column guarantees is that
    -- two principals cannot answer to the same name, which is what makes
    -- binding a record's source to its author (phase 3) mean anything.
    subject      TEXT NOT NULL UNIQUE,
    kind         TEXT NOT NULL CHECK (kind IN ('api_key', 'user')),
    display_name TEXT NOT NULL DEFAULT '',
    -- Hex SHA-256 of the bearer token. Non-null only for kind='api_key'; the
    -- plaintext is shown once at creation and never stored.
    --
    -- The design called for Argon2id and this is deliberately not that. A slow,
    -- salted hash earns its cost against a password a human chose, where the
    -- guessable space is small. These keys are minted by the server from 256
    -- bits of crypto/rand (internal/auth/apikey.go) and never supplied by a
    -- user, so there is no space to search: a stolen dump yields nothing to
    -- either hash. What Argon2id would cost is real, though -- a salted hash
    -- cannot be looked up by value, so every request would need a row scan or a
    -- second lookup column, and each verification would burn ~64 MiB and tens
    -- of milliseconds on the hot path of a store built for CI-scale write
    -- volume. Authentication stays one indexed equality check.
    --
    -- This holds only while keys are server-minted. An API that lets a caller
    -- choose its own key would put a guessable secret behind a fast hash and
    -- would have to bring Argon2id back with it.
    key_hash     TEXT,
    -- Revocation without deletion: a disabled key stops authenticating but its
    -- principal survives, so evidence already attributed to it still names
    -- something.
    disabled_at  TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ,
    CHECK ((kind = 'api_key') = (key_hash IS NOT NULL))
);

-- The authentication path in one index: hash the presented token, look it up.
-- Unique because two principals sharing a key would make the answer to "who is
-- calling" ambiguous, which is the whole thing this table exists to fix.
CREATE UNIQUE INDEX idx_principals_key_hash ON principals (key_hash)
    WHERE key_hash IS NOT NULL;

CREATE TABLE role_bindings (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    principal_id UUID NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
    -- Roles are fixed sets defined in code (internal/auth/role.go), so this is
    -- a CHECK and not a table: there are four of them against ten permissions,
    -- and a role-CRUD surface would be larger than the thing it governs.
    role         TEXT NOT NULL CHECK (role IN ('viewer','contributor','ci','admin')),
    -- Reserved for per-repo grants. '*' means store-wide, which is the only
    -- value written today; every authorization check asserts it, so a
    -- half-implemented scope cannot silently widen access. It is carried from
    -- the start because adding a column to a table with live grants is a worse
    -- migration than defaulting one nobody uses yet.
    scope        TEXT NOT NULL DEFAULT '*',
    granted_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Who granted it. Nullable because the bootstrap admin is granted its role
    -- by the server itself, before any principal exists to credit.
    granted_by   UUID REFERENCES principals(id),
    UNIQUE (principal_id, role, scope)
);

CREATE INDEX idx_role_bindings_principal ON role_bindings (principal_id);
