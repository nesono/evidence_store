-- Logins in flight at the moment of a rollback are lost, and the people making
-- them see one failure and start again. There is nothing here worth preserving
-- past that.
DROP TABLE IF EXISTS saml_requests;
