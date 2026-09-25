package db

import (
	"fmt"

	"autogateway/internal/models"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V2_8_3_PartialUniqueAliasCandidate replaces the plain UNIQUE index
// `idx_alias_group_model` on (alias, group_id, real_model) with a partial one
// covering **active** rows only (`deleted_at IS NULL`).
//
// Why: model_aliases uses GORM soft delete, but a schema-level unique index
// counts tombstones. Deleting a candidate and re-adding the same triple then
// dies on "UNIQUE constraint failed", which the handler turns into a generic
// 400. The edit drawer hits this on its own happy path: 删除别名 replaces the
// candidate set with [] (soft delete), and 撤销 writes the very same triples
// back — so undo could never succeed.
//
// Same shape as V2_5_17 (groups.name): the `uniqueIndex` tag is dropped from
// models.ModelAlias so AutoMigrate stops recreating the non-partial index on
// every boot, and this migration owns the constraint.
//
// Idempotent: skips when the partial index already exists. MUST run AFTER
// AutoMigrate, and after V1_2_0_DedupModelAliases (which clears live
// collisions so the CREATE below cannot fail).
func V2_8_3_PartialUniqueAliasCandidate(db *gorm.DB) error {
	if !db.Migrator().HasTable(&models.ModelAlias{}) {
		return nil
	}
	dialect := db.Dialector.Name()

	switch dialect {
	case "sqlite", "postgres":
		return ensurePartialUniqueAliasCandidate(db, dialect)
	case "mysql":
		// MySQL has no partial indexes; drop the legacy unique and let the
		// application layer guard uniqueness.
		logrus.Warn(
			"V2_8_3: MySQL detected — dropping legacy idx_alias_group_model UNIQUE index. " +
				"Alias candidate uniqueness now relies on application-layer checks.",
		)
		_ = db.Exec("ALTER TABLE model_aliases DROP INDEX idx_alias_group_model").Error
		return nil
	default:
		logrus.Warnf("V2_8_3: unknown dialect %q, skipping partial unique migration", dialect)
		return nil
	}
}

// ensurePartialUniqueAliasCandidate drops the tombstone-blocking index and
// creates the partial one. SQLite and Postgres agree on the SQL; only the
// introspection differs (handled by indexExists).
func ensurePartialUniqueAliasCandidate(db *gorm.DB, dialect string) error {
	const partialIdx = "idx_alias_group_model_active"
	const legacyIdx = "idx_alias_group_model"

	exists, err := indexExists(db, dialect, "model_aliases", partialIdx)
	if err != nil {
		return fmt.Errorf("check partial index existence: %w", err)
	}
	if exists {
		return nil
	}

	if err := db.Exec(fmt.Sprintf("DROP INDEX IF EXISTS %s", legacyIdx)).Error; err != nil {
		if dialect == "postgres" {
			_ = db.Exec("ALTER TABLE model_aliases DROP CONSTRAINT IF EXISTS " + legacyIdx).Error
		} else {
			return fmt.Errorf("drop legacy unique index: %w", err)
		}
	}

	sql := fmt.Sprintf(
		"CREATE UNIQUE INDEX %s ON model_aliases(alias, group_id, real_model) WHERE deleted_at IS NULL",
		partialIdx,
	)
	if err := db.Exec(sql).Error; err != nil {
		return fmt.Errorf("create partial unique index: %w", err)
	}
	logrus.Infof("V2_8_3: created partial unique index %s on model_aliases(alias, group_id, real_model)", partialIdx)
	return nil
}
