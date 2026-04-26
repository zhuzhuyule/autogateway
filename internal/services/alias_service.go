package services

import (
	"context"
	"fmt"
	"strings"

	app_errors "autogateway/internal/errors"
	"autogateway/internal/models"

	"gorm.io/gorm"
)

// ReservedAliases is the fixed list of aliases used by the smart "auto"
// routing path. They are seeded as placeholder rows (group_id=0) so the
// frontend always shows them, and the alias CRUD service refuses to
// rename / delete them.
var ReservedAliases = []string{"auto-simple", "auto-medium", "auto-complex"}

// AliasService manages CRUD on model_aliases plus seeding of reserved
// aliases. Routing decisions live in internal/router_engine — this
// service is purely the persistence layer.
type AliasService struct {
	db *gorm.DB
}

func NewAliasService(db *gorm.DB) *AliasService {
	return &AliasService{db: db}
}

// EnsureReservedSeeded inserts the three reserved aliases as placeholder
// rows if they are missing. Called once on app start.
func (s *AliasService) EnsureReservedSeeded(ctx context.Context) error {
	for _, name := range ReservedAliases {
		var count int64
		if err := s.db.WithContext(ctx).Model(&models.ModelAlias{}).
			Where("alias = ? AND is_reserved = ?", name, true).
			Count(&count).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
		if count > 0 {
			continue
		}
		seed := &models.ModelAlias{
			Alias:      name,
			GroupID:    0,
			RealModel:  "",
			Weight:     1,
			Priority:   100,
			Enabled:    true,
			IsReserved: true,
		}
		if err := s.db.WithContext(ctx).Create(seed).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
	}
	return nil
}

// ListAll returns every row, sorted alias asc, weight desc, priority asc.
func (s *AliasService) ListAll(ctx context.Context) ([]models.ModelAlias, error) {
	var rows []models.ModelAlias
	if err := s.db.WithContext(ctx).
		Order("alias asc, weight desc, priority asc, id asc").
		Find(&rows).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	return rows, nil
}

// ListByAlias returns the candidate pool for a given alias (excludes the
// reserved placeholder row with group_id=0).
func (s *AliasService) ListByAlias(ctx context.Context, alias string) ([]models.ModelAlias, error) {
	var rows []models.ModelAlias
	if err := s.db.WithContext(ctx).
		Where("alias = ? AND group_id <> 0", alias).
		Order("weight desc, priority asc, id asc").
		Find(&rows).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	return rows, nil
}

// CreateRequest is the JSON payload for POST /api/aliases.
type AliasCreateRequest struct {
	Alias     string `json:"alias"`
	GroupID   uint   `json:"group_id"`
	RealModel string `json:"real_model"`
	Weight    int    `json:"weight"`
	Priority  int    `json:"priority"`
	Enabled   *bool  `json:"enabled"`
}

func (s *AliasService) Create(ctx context.Context, req AliasCreateRequest) (*models.ModelAlias, error) {
	alias := strings.TrimSpace(req.Alias)
	real := strings.TrimSpace(req.RealModel)
	if alias == "" || real == "" || req.GroupID == 0 {
		return nil, fmt.Errorf("alias, group_id and real_model are required")
	}
	row := &models.ModelAlias{
		Alias:     alias,
		GroupID:   req.GroupID,
		RealModel: real,
		Weight:    nonZero(req.Weight, 1),
		Priority:  nonZero(req.Priority, 100),
		Enabled:   true,
	}
	if req.Enabled != nil {
		row.Enabled = *req.Enabled
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	return row, nil
}

// AliasUpdateRequest is the JSON payload for PUT /api/aliases/{id}.
// Only weight / priority / enabled may be updated; alias / group / real_model
// are immutable for a row (delete + recreate to change them).
type AliasUpdateRequest struct {
	Weight   *int  `json:"weight"`
	Priority *int  `json:"priority"`
	Enabled  *bool `json:"enabled"`
}

func (s *AliasService) Update(ctx context.Context, id uint, req AliasUpdateRequest) (*models.ModelAlias, error) {
	var row models.ModelAlias
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	updates := map[string]any{}
	if req.Weight != nil {
		updates["weight"] = *req.Weight
	}
	if req.Priority != nil {
		updates["priority"] = *req.Priority
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if len(updates) == 0 {
		return &row, nil
	}
	if err := s.db.WithContext(ctx).Model(&row).Updates(updates).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	return &row, nil
}

// Delete removes an alias row. Reserved placeholder rows (group_id=0,
// is_reserved=true) cannot be deleted; their members can be removed
// individually instead.
func (s *AliasService) Delete(ctx context.Context, id uint) error {
	var row models.ModelAlias
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		return app_errors.ParseDBError(err)
	}
	if row.IsReserved && row.GroupID == 0 {
		return fmt.Errorf("reserved alias placeholder cannot be deleted")
	}
	if err := s.db.WithContext(ctx).Delete(&row).Error; err != nil {
		return app_errors.ParseDBError(err)
	}
	return nil
}

func nonZero(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}
