-- Subscription export was removed: Prism is a relay gateway and no longer
-- mints export profiles or serves /sub/{token}. Drop the table it used.
DROP TABLE IF EXISTS export_profiles;
