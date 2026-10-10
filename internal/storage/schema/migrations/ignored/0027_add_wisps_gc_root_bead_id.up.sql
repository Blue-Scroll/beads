-- Ignored migration 0027 (TOWN-ONLY): ensure wisps.gc_root_bead_id and its
-- index exist on every clone (vn-s54d6fy).
--
-- Synced migration 0067 adds the column and index to wisps, but wisps is
-- dolt-ignored, so its schema is clone-local. A clone that bootstraps from a
-- remote whose schema_migrations cursor is already at 0067 adopts the cursor
-- without running 0067, and its wisps table (made by ignored/0001) would lack
-- the column. Every gc.root_bead_id metadata filter over wisps would then fail
-- with Error 1054. Same mechanism and fix as ignored/0020 (storage_class).
--
-- Upstream has its own ignored 0027, so this number moves at the next rebase
-- of `town`; see 0067's header for the repair.
--
-- Guarded, so it no-ops where synced 0067 already ran and where the clone has
-- no wisps table yet. The definition must stay byte-for-byte the same as
-- 0067's wisps half.
SET @needs_add = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps') > 0
    AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND COLUMN_NAME = 'gc_root_bead_id') = 0,
    1, 0
);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE wisps ADD COLUMN gc_root_bead_id VARCHAR(255) GENERATED ALWAYS AS (LEFT(JSON_UNQUOTE(JSON_EXTRACT(metadata, ''$."gc.root_bead_id"'')), 255)) STORED',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_index = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps') > 0
    AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND INDEX_NAME = 'idx_wisps_gc_root_bead_id') = 0,
    1, 0
);
SET @sql = IF(@needs_index = 1,
    'CREATE INDEX idx_wisps_gc_root_bead_id ON wisps (gc_root_bead_id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
