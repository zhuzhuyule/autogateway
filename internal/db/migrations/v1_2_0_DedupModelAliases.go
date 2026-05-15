package db

import (
	"autogateway/internal/models"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// V1_2_0_DedupModelAliases removes duplicate (alias, group_id, real_model)
// rows from model_aliases so the subsequent AutoMigrate can safely add the
// unique index idx_alias_group_model. Keeps the lowest id per triple.
//
// MUST be called BEFORE AutoMigrate. No-op on fresh installs (table absent)
// or when no duplicates are present.
func V1_2_0_DedupModelAliases(db *gorm.DB) error {
	if !db.Migrator().HasTable(&models.ModelAlias{}) {
		return nil
	}

	type dupGroup struct {
		Alias     string
		GroupID   uint
		RealModel string
		KeepID    uint
	}
	var groups []dupGroup
	if err := db.Raw(`
		SELECT alias, group_id, real_model, MIN(id) AS keep_id
		FROM model_aliases
		GROUP BY alias, group_id, real_model
		HAVING COUNT(*) > 1
	`).Scan(&groups).Error; err != nil {
		return err
	}
	if len(groups) == 0 {
		return nil
	}
	logrus.Warnf("V1_2_0_DedupModelAliases: %d duplicate triples found, cleaning up before adding unique index", len(groups))

	for _, g := range groups {
		res := db.Exec(`
			DELETE FROM model_aliases
			WHERE alias = ? AND group_id = ? AND real_model = ? AND id <> ?
		`, g.Alias, g.GroupID, g.RealModel, g.KeepID)
		if res.Error != nil {
			return res.Error
		}
		logrus.Infof("  - %q/%d/%q: deleted %d duplicates (kept id=%d)",
			g.Alias, g.GroupID, g.RealModel, res.RowsAffected, g.KeepID)
	}
	return nil
}
