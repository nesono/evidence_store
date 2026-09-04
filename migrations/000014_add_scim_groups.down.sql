DELETE FROM role_bindings WHERE source = 'scim';
ALTER TABLE role_bindings DROP CONSTRAINT role_bindings_source_check;
ALTER TABLE role_bindings ADD CONSTRAINT role_bindings_source_check
    CHECK (source IN ('local', 'idp'));
DROP TABLE scim_group_members;
DROP TABLE scim_groups;
