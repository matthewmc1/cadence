-- 0004_auth (down)
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS login_tokens;
DROP TABLE IF EXISTS accounts;
ALTER TABLE users
  DROP COLUMN IF EXISTS created_at,
  ADD COLUMN name text NOT NULL DEFAULT '',
  ADD COLUMN initial text NOT NULL DEFAULT '',
  ADD COLUMN color text NOT NULL DEFAULT '';
