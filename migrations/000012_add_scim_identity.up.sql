-- What a directory knows about somebody before they have ever logged in.
--
-- Single sign-on creates a principal at first login and reconciles their roles
-- from that login's token. Both halves wait on a login happening, which is why
-- there is no offboarding: someone who leaves the company is disabled at the
-- provider, their next login would be refused, and the next login never comes.
-- The account here stays enabled, and so does any session they left open.
--
-- SCIM is the provider telling us without waiting for the person. These columns
-- are what it needs to name somebody this store has never seen.
ALTER TABLE principals ADD COLUMN scim_id TEXT;
-- What the provider calls them at their end. Kept for the provider's own
-- filter queries, not matched on here: Entra maps it from mailNickname by
-- default and a tenant may map it to anything.
ALTER TABLE principals ADD COLUMN scim_external_id TEXT;
-- SCIM userName, the closest thing the protocol has to a login name. Usually a
-- UPN, which is not always the address the ID token carries -- hence both this
-- and subject are consulted when a login looks for the row provisioned for it.
ALTER TABLE principals ADD COLUMN user_name TEXT;

-- The id this store hands back to the provider, and the key every later SCIM
-- request addresses the person by.
CREATE UNIQUE INDEX idx_principals_scim_id ON principals (scim_id)
    WHERE scim_id IS NOT NULL;
-- SCIM requires userName to be unique, and a provisioner that could create two
-- of them would have no way to say which one it meant afterwards.
CREATE UNIQUE INDEX idx_principals_user_name ON principals (user_name)
    WHERE user_name IS NOT NULL;

-- Claiming: how a provisioned row and a login become one person.
--
-- A provisioned principal starts with no external_id, because the provider
-- cannot supply one. Entra's sub is pairwise -- invented per application at
-- first login -- so SCIM has never seen it and cannot send it. The first login
-- that recognises itself in a provisioned row writes its own "<issuer>|<sub>"
-- there, and from then on matches on it like any other.
--
-- This index is what keeps that lookup cheap and what makes "unclaimed" a
-- state the database can find rather than a scan.
CREATE INDEX idx_principals_unclaimed_scim ON principals (subject, user_name)
    WHERE scim_id IS NOT NULL AND external_id IS NULL;
