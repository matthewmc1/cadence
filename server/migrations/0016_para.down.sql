-- 0016_para (down). Areas fold back into 'internal' so the narrower CHECK holds.
UPDATE clients SET kind = 'internal' WHERE kind = 'area';
ALTER TABLE clients DROP CONSTRAINT clients_kind_check;
ALTER TABLE clients ADD CONSTRAINT clients_kind_check CHECK (kind IN ('client', 'internal'));
ALTER TABLE clients DROP COLUMN standard;
ALTER TABLE projects DROP COLUMN outcome;
