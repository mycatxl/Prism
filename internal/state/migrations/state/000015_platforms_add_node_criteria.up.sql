-- WP11: the platform node-selection criteria the operator picks from the
-- inventory. They are ANDed with each other and the values inside one criterion
-- are alternatives; an empty list means "do not restrict on this criterion".
-- The legacy regex_filters_json and region_filters_json keep their own
-- semantics, so a platform written before this migration filters unchanged.
ALTER TABLE platforms ADD COLUMN ip_types_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE platforms ADD COLUMN purity_bands_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE platforms ADD COLUMN subscription_filters_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE platforms ADD COLUMN protocols_json TEXT NOT NULL DEFAULT '[]';
