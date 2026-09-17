-- 0016_para — PARA (Projects · Areas · Resources · Archives).
--
-- Projects gain an `outcome`: the one sentence that says why the project
-- exists and what finishing it looks like. With `due` (0001) that is PARA's
-- definition of a project — an outcome with a deadline — and every work item
-- under it inherits its "why" from here. Archiving uses archived_at (0002),
-- which has been in the schema unread until now.
--
-- Areas are NOT a new table: a Client already sits above Project with a
-- cadence target, which is what an area of responsibility is. kind gains
-- 'area' and `standard` records the standard the area is held to ("books
-- closed by the 5th"), beside expected_touch_days which says how often.
--
-- EXPAND only (see 0002): NOT NULL text with a constant default is a
-- metadata-only change, and widening a CHECK never invalidates a stored row,
-- so old instances keep serving while this runs.

ALTER TABLE projects ADD COLUMN outcome text NOT NULL DEFAULT '';

ALTER TABLE clients ADD COLUMN standard text NOT NULL DEFAULT '';
ALTER TABLE clients DROP CONSTRAINT clients_kind_check;
ALTER TABLE clients ADD CONSTRAINT clients_kind_check CHECK (kind IN ('client', 'internal', 'area'));
