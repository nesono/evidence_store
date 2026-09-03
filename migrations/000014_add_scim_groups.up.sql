-- The groups a directory keeps, and what they grant here.
--
-- Stored rather than collapsed straight into roles because SCIM requires them
-- to be readable back: a provisioner asks for a group by name and expects its
-- own membership list returned. A group that maps to no role still has to
-- exist, and still has to list its members, or the provisioner concludes its
-- last write was lost and sends it again forever.
CREATE TABLE scim_groups (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- The id this store hands back, and what every later request addresses.
    scim_id      TEXT NOT NULL UNIQUE,
    -- The provisioner's own name for the group. Echoed, never matched on.
    external_id  TEXT,
    -- What the role map is keyed by, so this is the string that decides what
    -- membership grants.
    display_name TEXT NOT NULL UNIQUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE scim_group_members (
    group_id     UUID NOT NULL REFERENCES scim_groups(id) ON DELETE CASCADE,
    principal_id UUID NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, principal_id)
);

-- Serves the reconciliation after a membership change: which groups is this
-- person in, so which roles should they now hold.
CREATE INDEX idx_scim_group_members_principal ON scim_group_members (principal_id);

-- A third source of a role binding.
--
-- Reusing 'idp' would make the two paths delete each other's work: a login
-- reconciles the idp rows to the groups in its token, which would drop
-- everything the directory had granted, and the next sync would put it back.
-- Effective permissions are the union of the three, which is what both paths
-- already assume.
ALTER TABLE role_bindings DROP CONSTRAINT role_bindings_source_check;
ALTER TABLE role_bindings ADD CONSTRAINT role_bindings_source_check
    CHECK (source IN ('local', 'idp', 'scim'));
