ALTER TABLE hysteria_profiles ADD COLUMN legacy_compat INTEGER NOT NULL DEFAULT 0 CHECK(legacy_compat IN (0,1));
