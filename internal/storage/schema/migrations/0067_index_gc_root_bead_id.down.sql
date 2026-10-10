-- Reverse of 0067: drop the gc_root_bead_id index and generated column from
-- issues and wisps. Each step is guarded, so a partly-applied or issues-only
-- workspace rolls back as safely as it migrated up (0060 precedent).
-- Pair it with a binary rollback: a 0067-era bd emits `gc_root_bead_id = ?`
-- for a gc.root_bead_id metadata filter, and against a table without the
-- column that query fails with Error 1054.

SET @has_index = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND INDEX_NAME = 'idx_issues_gc_root_bead_id'
);
SET @sql = IF(@has_index > 0,
    'DROP INDEX idx_issues_gc_root_bead_id ON issues',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_col = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'gc_root_bead_id'
);
SET @sql = IF(@has_col > 0,
    'ALTER TABLE issues DROP COLUMN gc_root_bead_id',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_index = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisps'
      AND INDEX_NAME = 'idx_wisps_gc_root_bead_id'
);
SET @sql = IF(@has_index > 0,
    'DROP INDEX idx_wisps_gc_root_bead_id ON wisps',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_col = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisps'
      AND COLUMN_NAME = 'gc_root_bead_id'
);
SET @sql = IF(@has_col > 0,
    'ALTER TABLE wisps DROP COLUMN gc_root_bead_id',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
