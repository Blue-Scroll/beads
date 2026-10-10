-- Migration 0067 (TOWN-ONLY, Blue-Scroll/beads `town`): index the
-- gc.root_bead_id metadata key on issues and wisps (vn-s54d6fy).
--
-- gc's DirectMembers lists every bead whose metadata gc.root_bead_id equals a
-- root, closed included, on every fan-out, retry, drain and scope step. With
-- no status filter that was a full scan with a JSON probe per row: 10 to 15 s
-- per call on a 71,534-row ledger, 20 s on the live server under load.
--
-- WHY A STORED GENERATED COLUMN. Dolt does not match a JSON expression to an
-- index by itself, and an index needs a column. A VIRTUAL column made the
-- lookup instant but made every OTHER JSON_EXTRACT(metadata, ...) scan 3x
-- slower (37 s against 12 s), because the engine recomputes a virtual column
-- on every row read. STORED pays once: one table rewrite here, then each
-- write keeps the column current.
--
-- WHY LEFT(..., 255). A stored value longer than the column would make the
-- WRITE fail, so a long metadata value must never be able to block a bead
-- update. The column is only a narrowing key: sqlbuild.AppendMetadataClauses
-- pairs `gc_root_bead_id = ?` with the original JSON predicate, which stays
-- the exact match. A JSON null reads as the string 'null' and a missing key
-- as NULL, exactly as JSON_UNQUOTE(JSON_EXTRACT(...)) already did.
--
-- THIS NUMBER IS OURS, NOT UPSTREAM'S. Upstream already has its own main
-- 0067 and ignored 0027 (it was at main 0069 on 2026-10-09), so the next
-- rebase of `town` collides for certain. Then this file and ignored/0027 are
-- renamed past upstream's last ones, and every ledger has its rows for main 67
-- and ignored 27 deleted BEFORE the rebased binary first opens it; otherwise
-- that binary reads 67 as applied and silently skips upstream's 0067. The
-- guards below make the renamed files a no-op on a ledger that already has
-- the column and index, so they only record their new numbers. The full
-- recipe is in gas-city docs/tool-forks.md, "beads `town` has its own
-- migration 0067".
--
-- Guarded on INFORMATION_SCHEMA probes (0054/0060 precedent), so it is
-- idempotent on replay. The wisps half also no-ops when the clone has no
-- wisps table. wisps is dolt-ignored, so a clone whose cursor arrives past
-- this version never runs this file: ignored/0027 is its twin.

-- issues.gc_root_bead_id
SET @needs_add = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'gc_root_bead_id'
);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE issues ADD COLUMN gc_root_bead_id VARCHAR(255) GENERATED ALWAYS AS (LEFT(JSON_UNQUOTE(JSON_EXTRACT(metadata, ''$."gc.root_bead_id"'')), 255)) STORED',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_index = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND INDEX_NAME = 'idx_issues_gc_root_bead_id'
);
SET @sql = IF(@needs_index = 1,
    'CREATE INDEX idx_issues_gc_root_bead_id ON issues (gc_root_bead_id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- wisps.gc_root_bead_id: wisps shares the issues row shape, and the shared
-- metadata filter emits the same column predicate against both tables.
SET @has_wisps = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps'
);
SET @needs_add = IF(@has_wisps > 0 AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND COLUMN_NAME = 'gc_root_bead_id') = 0,
    1, 0);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE wisps ADD COLUMN gc_root_bead_id VARCHAR(255) GENERATED ALWAYS AS (LEFT(JSON_UNQUOTE(JSON_EXTRACT(metadata, ''$."gc.root_bead_id"'')), 255)) STORED',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_index = IF(@has_wisps > 0 AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND INDEX_NAME = 'idx_wisps_gc_root_bead_id') = 0,
    1, 0);
SET @sql = IF(@needs_index = 1,
    'CREATE INDEX idx_wisps_gc_root_bead_id ON wisps (gc_root_bead_id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
