-- role_bindings first: it references principals, and dropping the referenced
-- table out from under it would fail.
DROP TABLE IF EXISTS role_bindings;
DROP TABLE IF EXISTS principals;
