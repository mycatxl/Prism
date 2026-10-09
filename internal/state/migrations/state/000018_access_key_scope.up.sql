ALTER TABLE access_keys ADD COLUMN scope TEXT NOT NULL DEFAULT 'proxy' CHECK (scope IN ('proxy','admin'));
