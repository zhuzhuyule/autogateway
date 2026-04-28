package services

import (
	"context"
	"time"

	"autogateway/internal/models"

	"gorm.io/gorm"
)

// AliasSuggestion is one row in the response: a client-supplied model that
// repeatedly failed with 404/405 over the lookback window and currently has
// no matching alias.
type AliasSuggestion struct {
	Model    string    `json:"model"`
	Count    int64     `json:"count"`
	LastSeen time.Time `json:"last_seen"`
}

// AliasSuggestionService scans request_logs for models the gateway didn't
// know and weren't already aliased.
type AliasSuggestionService struct {
	db *gorm.DB
}

func NewAliasSuggestionService(db *gorm.DB) *AliasSuggestionService {
	return &AliasSuggestionService{db: db}
}

// Suggest returns up to 20 unmatched models ordered by failure count desc.
func (s *AliasSuggestionService) Suggest(ctx context.Context, lookback time.Duration) ([]AliasSuggestion, error) {
	since := time.Now().Add(-lookback)
	// Use string for LastSeen so SQLite (which returns timestamps as text) and
	// MySQL/Postgres (which return time.Time) both scan cleanly via a manual parse.
	type row struct {
		Model    string
		Count    int64
		LastSeen string
	}
	var rows []row
	if err := s.db.WithContext(ctx).
		Model(&models.RequestLog{}).
		Select("model, COUNT(*) as count, MAX(timestamp) as last_seen").
		Where("status_code IN (404, 405) AND is_success = ? AND model <> '' AND timestamp >= ?", false, since).
		Group("model").
		Order("count DESC").
		Limit(20).
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	if len(rows) == 0 {
		return nil, nil
	}

	candidates := make([]string, 0, len(rows))
	for _, r := range rows {
		candidates = append(candidates, r.Model)
	}
	var aliased []string
	if err := s.db.WithContext(ctx).
		Model(&models.ModelAlias{}).
		Where("alias IN ? AND enabled = ?", candidates, true).
		Distinct("alias").
		Pluck("alias", &aliased).Error; err != nil {
		return nil, err
	}
	skip := make(map[string]bool, len(aliased))
	for _, a := range aliased {
		skip[a] = true
	}

	out := make([]AliasSuggestion, 0, len(rows))
	for _, r := range rows {
		if skip[r.Model] {
			continue
		}
		// Best-effort parse of the timestamp string; zero value on failure.
		var lastSeen time.Time
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999 -0700 MST", "2006-01-02T15:04:05Z07:00", "2006-01-02 15:04:05"} {
			if t, err := time.Parse(layout, r.LastSeen); err == nil {
				lastSeen = t
				break
			}
		}
		out = append(out, AliasSuggestion{Model: r.Model, Count: r.Count, LastSeen: lastSeen})
	}
	return out, nil
}
