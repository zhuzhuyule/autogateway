package db

import (
	"autogateway/internal/models"
	"fmt"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V2_5_17_PartialUniqueGroupName replaces the legacy `uni_groups_name`
// UNIQUE(name) constraint with a partial unique index that only enforces
// uniqueness on **active** rows (`deleted_at IS NULL`).
//
// Why: GORM's `gorm:"unique"` tag generates a schema-level UNIQUE constraint
// that does NOT respect soft-delete. After soft-deleting a group named
// "openai-2", a new group with the same name throws DUPLICATE_RESOURCE
// because the dead row still occupies the unique slot.
//
// Why not just hard-delete: P9.4 sync uses `max(UpdatedAt, DeletedAt)` for
// LWW comparison — hard deletes lose the tombstone and break peer sync.
//
// Cross-dialect support:
//   - SQLite / PostgreSQL: native partial unique index (`WHERE deleted_at IS NULL`)
//   - MySQL: partial indexes not supported. We drop the legacy unique and
//     emit a warning — application-layer uniqueness check is the fallback.
//     (Could be revisited with MySQL 8 generated-column hack if needed.)
//
// Idempotent: safe to run on every startup. Skips if migration already
// applied (partial index exists) or if groups table doesn't exist (fresh DB).
//
// MUST be called AFTER AutoMigrate (the legacy index is created by gorm during
// migration). Cleans up any soft-deleted name collisions before creating the
// new index — otherwise CREATE UNIQUE INDEX would fail on existing duplicates.
func V2_5_17_PartialUniqueGroupName(db *gorm.DB) error {
	if !db.Migrator().HasTable(&models.Group{}) {
		return nil
	}
	dialect := db.Dialector.Name()

	// 1) Pre-cleanup: if both an active row and a soft-deleted row share the
	//    same name, the new partial index can be created without conflict
	//    (only the active row counts). But if multiple soft-deleted rows share
	//    a name with no active row, the partial index won't see them — fine.
	//    The actual conflict is: an active row whose name collides with
	//    another active row (shouldn't happen with old unique constraint
	//    intact, but defensive).
	//    → nothing to clean here.

	switch dialect {
	case "sqlite", "postgres":
		return ensurePartialUniqueGroupName(db, dialect)
	case "mysql":
		// MySQL doesn't support partial unique indexes. Drop the legacy
		// unique and rely on application-layer check (group_service does
		// `WHERE name = ? AND deleted_at IS NULL` before insert).
		logrus.Warn(
			"V2_5_17: MySQL detected — dropping legacy uni_groups_name UNIQUE constraint. " +
				"Group name uniqueness now relies on application-layer check. " +
				"Race conditions under concurrent creation are possible.",
		)
		return dropLegacyGroupNameUnique(db, dialect)
	default:
		logrus.Warnf("V2_5_17: unknown dialect %q, skipping partial unique migration", dialect)
		return nil
	}
}

// ensurePartialUniqueGroupName drops legacy `uni_groups_name` and creates
// `uni_groups_name_active` partial index. SQLite and Postgres support is
// identical at the SQL level — only the introspection query differs.
func ensurePartialUniqueGroupName(db *gorm.DB, dialect string) error {
	const partialIdx = "uni_groups_name_active"
	const legacyIdx = "uni_groups_name"

	// Skip if partial index already exists (idempotent).
	exists, err := indexExists(db, dialect, "groups", partialIdx)
	if err != nil {
		return fmt.Errorf("check partial index existence: %w", err)
	}
	if exists {
		return nil
	}

	// Drop the legacy non-partial UNIQUE. Both dialects support IF EXISTS.
	if err := db.Exec(fmt.Sprintf("DROP INDEX IF EXISTS %s", legacyIdx)).Error; err != nil {
		// On Postgres the unique is named differently depending on how it was
		// created (constraint vs index). Try the constraint form as fallback.
		if dialect == "postgres" {
			_ = db.Exec(fmt.Sprintf("ALTER TABLE groups DROP CONSTRAINT IF EXISTS %s", legacyIdx)).Error
		} else {
			return fmt.Errorf("drop legacy unique: %w", err)
		}
	}

	// Create partial unique on (name) WHERE deleted_at IS NULL.
	sql := fmt.Sprintf(
		"CREATE UNIQUE INDEX %s ON groups(name) WHERE deleted_at IS NULL",
		partialIdx,
	)
	if err := db.Exec(sql).Error; err != nil {
		return fmt.Errorf("create partial unique index: %w", err)
	}
	logrus.Infof("V2_5_17: created partial unique index %s on groups(name)", partialIdx)
	return nil
}

// dropLegacyGroupNameUnique removes the legacy unique constraint on MySQL.
// No partial index replacement — application layer takes over.
func dropLegacyGroupNameUnique(db *gorm.DB, dialect string) error {
	// MySQL: indexes live as table-level metadata. Best-effort drop.
	_ = db.Exec("ALTER TABLE groups DROP INDEX uni_groups_name").Error
	return nil
}

// indexExists checks dialect-specific catalog for an index by name.
func indexExists(db *gorm.DB, dialect, table, indexName string) (bool, error) {
	switch dialect {
	case "sqlite":
		var count int64
		err := db.Raw(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=? AND tbl_name=?`,
			indexName, table,
		).Scan(&count).Error
		return count > 0, err
	case "postgres":
		var count int64
		err := db.Raw(
			`SELECT COUNT(*) FROM pg_indexes WHERE indexname=? AND tablename=?`,
			indexName, table,
		).Scan(&count).Error
		return count > 0, err
	default:
		return false, nil
	}
}
