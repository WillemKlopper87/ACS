package subscriptions

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"acs/internal/store"
)

// Subscription is a row of usp_subscriptions -- the controller's
// desired-state record for a USP notification subscription that should
// exist on a device (migration 0055). It is desired state only: there is
// no requirement to preserve a subscription's history, only whether it
// currently should exist on the device. ID is the UUID the controller
// itself generates and writes verbatim into the agent's
// Device.LocalAgent.Subscription.{i}.ID -- it *is* the wire id, not a
// separate correlation key.
type Subscription struct {
	ID            string
	DeviceID      string
	NotifType     string
	ReferenceList []string
	Persistent    bool
	CreatedBy     string
	CreatedAt     time.Time
}

// Repository is a thin, ordinary CRUD layer over usp_subscriptions. It
// carries no diff/decision logic -- that lives entirely in Reconcile and in
// the USP-specific glue that calls it.
type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

const subscriptionColumns = `id, device_id, notif_type, reference_list, persistent, created_by, created_at`

// Create inserts a new desired-state subscription row.
func (r *Repository) Create(ctx context.Context, sub Subscription) error {
	// store.StringArray(nil).Value() encodes as SQL NULL, which
	// reference_list's NOT NULL constraint rejects -- a nil ReferenceList
	// here means "no paths given", not "write NULL", so normalize it to an
	// empty (non-null) array (same discipline as LinkUspAgent's
	// supportedProtocolVersions in internal/devices/usp_agents.go).
	refs := sub.ReferenceList
	if refs == nil {
		refs = []string{}
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO usp_subscriptions (id, device_id, notif_type, reference_list, persistent, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, sub.ID, sub.DeviceID, sub.NotifType, store.StringArray(refs), sub.Persistent, sub.CreatedBy, sub.CreatedAt)
	if err != nil {
		return fmt.Errorf("create subscription: %w", err)
	}
	return nil
}

// ByDevice returns every desired-state subscription row for a device.
func (r *Repository) ByDevice(ctx context.Context, deviceID string) ([]Subscription, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+subscriptionColumns+` FROM usp_subscriptions WHERE device_id = $1`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("list subscriptions by device: %w", err)
	}
	defer rows.Close()

	var subs []Subscription
	for rows.Next() {
		sub, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		subs = append(subs, *sub)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list subscriptions by device: %w", err)
	}
	return subs, nil
}

// Delete removes a desired-state subscription row by id. Deleting an id
// with no matching row is not an error.
func (r *Repository) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM usp_subscriptions WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete subscription: %w", err)
	}
	return nil
}

// scanner abstracts *sql.Row and *sql.Rows for scanSubscription, mirroring
// internal/devices's scanner interface.
type scanner interface {
	Scan(dest ...any) error
}

func scanSubscription(s scanner) (*Subscription, error) {
	var sub Subscription
	var refs store.StringArray
	if err := s.Scan(&sub.ID, &sub.DeviceID, &sub.NotifType, &refs, &sub.Persistent, &sub.CreatedBy, &sub.CreatedAt); err != nil {
		return nil, fmt.Errorf("scan subscription: %w", err)
	}
	sub.ReferenceList = []string(refs)
	return &sub, nil
}
