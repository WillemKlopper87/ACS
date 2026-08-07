// Package dashboard owns per-operator customizable fleet-dashboard layouts
// (admin-platform backlog, migration 0034). The dashboard *data* (fleet
// health counts, grouped breakdowns, alarms, firmware/temperature) is
// computed on demand by cmd/api's dashboard handler from
// internal/devices, internal/tenancy, internal/firmware, and
// internal/parameters — this package only owns which widgets an operator
// chose to see and in what order, not the numbers inside them.
package dashboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// Widget is one entry in a saved layout — deliberately just an id +
// enabled flag (order is the array's own order) rather than a typed
// struct per widget kind, so adding a new widget type later needs no
// migration, only a new id both sides recognize.
type Widget struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

// DefaultWidgets is what a first-time operator (no saved layout yet) sees
// — every widget on, in a sensible triage order: the two existing Fleet
// Health breakdowns first, then the new grouped/firmware/alarm/temperature
// additions.
var DefaultWidgets = []Widget{
	{ID: "status", Enabled: true},
	{ID: "reachability", Enabled: true},
	{ID: "inform_recency", Enabled: true},
	{ID: "alarms", Enabled: true},
	{ID: "group_by", Enabled: true},
	{ID: "firmware", Enabled: true},
	{ID: "temperature", Enabled: true},
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// Layout returns an operator's saved widget list, or DefaultWidgets if
// they've never saved one.
func (r *Repository) Layout(ctx context.Context, operatorID string) ([]Widget, error) {
	var raw []byte
	err := r.db.QueryRowContext(ctx, `SELECT widgets FROM dashboard_layouts WHERE operator_id = $1`, operatorID).Scan(&raw)
	if err == sql.ErrNoRows {
		return DefaultWidgets, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load dashboard layout: %w", err)
	}
	var widgets []Widget
	if err := json.Unmarshal(raw, &widgets); err != nil {
		return nil, fmt.Errorf("unmarshal dashboard layout: %w", err)
	}
	return widgets, nil
}

// SaveLayout replaces an operator's saved widget list wholesale.
func (r *Repository) SaveLayout(ctx context.Context, operatorID string, widgets []Widget) error {
	raw, err := json.Marshal(widgets)
	if err != nil {
		return fmt.Errorf("marshal dashboard layout: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO dashboard_layouts (operator_id, widgets, updated_at)
		VALUES ($1, $2::jsonb, now())
		ON CONFLICT (operator_id) DO UPDATE SET widgets = EXCLUDED.widgets, updated_at = now()
	`, operatorID, raw)
	if err != nil {
		return fmt.Errorf("save dashboard layout: %w", err)
	}
	return nil
}
