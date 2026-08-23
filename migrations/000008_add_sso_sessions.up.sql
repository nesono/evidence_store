-- What a human's login leaves behind.
--
-- Phases 2-4 made a credential something with an owner that can be revoked on
-- the next request. A browser session has to work the same way, or "log this
-- user out now" means "wait for the cookie to expire" -- which would be a
-- strange answer from a store whose Access tab revokes an API key instantly.
-- Open question 1 in docs/rbac-design.md, settled in favour of the table.

CREATE TABLE sessions (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Hex SHA-256 of the cookie value, for the same reason principals.key_hash
    -- is: the token is minted here from 256 bits of crypto/rand, so there is
    -- nothing to brute-force, and authentication stays one indexed lookup.
    -- A database dump does not hand anybody a live session.
    token_hash   TEXT NOT NULL UNIQUE,
    principal_id UUID NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
    -- Sessions die with their principal. Deleting a person should not leave a
    -- cookie that still resolves to them.
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ,
    -- Enough to recognise a session in a list of them: "Chrome on the laptop"
    -- rather than a UUID. Free text from a client, so it is display only and
    -- nothing branches on it.
    user_agent   TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_sessions_principal ON sessions (principal_id);
-- Serves the sweep that removes what has expired.
CREATE INDEX idx_sessions_expires ON sessions (expires_at);

-- The IdP's own name for a person, "<issuer>|<sub>".
--
-- subject stays the readable one -- "user:alice@example.com" -- because it is
-- what lands in evidence.source and what a reader sees months later. But an
-- address is not an identity: people are renamed, and matching on the readable
-- name would either create a second principal for the same human or attach
-- their history to whoever inherited the address. The sub claim is the thing
-- an IdP promises is stable, so it is what the upsert matches on, leaving
-- subject free to be corrected on the next login.
ALTER TABLE principals ADD COLUMN external_id TEXT;
CREATE UNIQUE INDEX idx_principals_external_id ON principals (external_id)
    WHERE external_id IS NOT NULL;

-- Where a grant came from.
--
-- On every login the roles derived from the IdP's group claims are reconciled
-- to match what the token now says -- someone removed from eng-leads loses
-- admin at their next login. Roles an administrator granted by hand must
-- survive that, or the Access tab would be quietly undone by a login, and an
-- IdP that exposes no useful groups would leave nobody able to grant anything.
ALTER TABLE role_bindings ADD COLUMN source TEXT NOT NULL DEFAULT 'local'
    CHECK (source IN ('local', 'idp'));
