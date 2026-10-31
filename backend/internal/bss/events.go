package bss

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"acs/internal/store"
)

// EventDevice identifies the device an event is about.
type EventDevice struct {
	ID        string `json:"id"`
	OUISerial string `json:"oui_serial"`
}

// EventEnvelope is the one shape every integration event has, in webhooks
// and in replay (docs/integration/09-events.md).
type EventEnvelope struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	OccurredAt time.Time       `json:"occurred_at"`
	Sequence   int64           `json:"sequence"`
	CustomerID *string         `json:"customer_id"`
	AccountID  *string         `json:"account_id"`
	Device     *EventDevice    `json:"device"`
	Data       json.RawMessage `json:"data"`
}

// EventPage is a page of one customer's events after a cursor.
type EventPage struct {
	Events []EventEnvelope
	// PurgedThrough: events at or below this sequence were removed by
	// retention; a cursor below it has missed events (a gap).
	PurgedThrough int64
	// LatestSequence is the highest sequence number assigned so far.
	LatestSequence int64
}

// EventsAfter returns a customer's events with sequence > after, oldest
// first. customerID "" is the stream of events that belong to no customer.
// accounts, when non-empty, keeps only events of those accounts.
func (r *Repository) EventsAfter(ctx context.Context, customerID string, after int64, limit int, accounts []string) (*EventPage, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	page := &EventPage{Events: []EventEnvelope{}}
	err := r.db.QueryRowContext(ctx, `SELECT last_seq, purged_through FROM tmf_event_sequences WHERE scope = $1`, customerID).
		Scan(&page.LatestSequence, &page.PurgedThrough)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read event sequence: %w", err)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT e.id, e.event_type, e.event_time, e.seq, e.customer_id::text, e.account_id, e.device_id::text, d.oui_serial, e.payload
		  FROM tmf_events e LEFT JOIN devices d ON d.id = e.device_id
		 WHERE COALESCE(e.customer_id::text, '') = $1 AND e.seq > $2
		   AND ($4::text[] IS NULL OR e.account_id = ANY($4::text[]))
		 ORDER BY e.seq LIMIT $3`, customerID, after, limit, store.StringArray(accounts))
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var e EventEnvelope
		var customer, account, deviceID, ouiSerial sql.NullString
		if err := rows.Scan(&e.ID, &e.Type, &e.OccurredAt, &e.Sequence, &customer, &account, &deviceID, &ouiSerial, &e.Data); err != nil {
			return nil, err
		}
		if customer.Valid {
			e.CustomerID = &customer.String
		}
		if account.Valid {
			e.AccountID = &account.String
		}
		if deviceID.Valid {
			e.Device = &EventDevice{ID: deviceID.String, OUISerial: ouiSerial.String}
		}
		page.Events = append(page.Events, e)
	}
	return page, rows.Err()
}

// CreateCustomerEvent records an event that belongs to one known customer,
// such as a sale's onboarding progress. The account and device are attached
// only when they belong to that customer too (or to nobody), so an account
// reference or a device of another customer can never route the event into
// that customer's stream or webhooks. An empty customerID falls back to
// CreateEvent, which infers the customer.
func (r *Repository) CreateCustomerEvent(ctx context.Context, customerID, sourceKey, accountID, deviceID, eventType string, payload json.RawMessage, at time.Time) (*EventRecord, error) {
	if customerID == "" {
		return r.CreateEvent(ctx, sourceKey, accountID, deviceID, "", eventType, payload, at)
	}
	var e EventRecord
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO tmf_events (id, source_key, customer_id, account_id, device_id, event_type, event_time, payload)
		SELECT gen_random_uuid(), $1::text, $2::text::uuid,
		       CASE WHEN COALESCE((SELECT customer_id::text FROM bss_accounts WHERE account_id = NULLIF($3::text, '')), $2::text) = $2::text
		            THEN NULLIF($3::text, '') END,
		       CASE WHEN COALESCE((SELECT customer_id::text FROM devices WHERE id = NULLIF($4::text, '')::uuid), $2::text) = $2::text
		            THEN NULLIF($4::text, '')::uuid END,
		       $5::text, $6::timestamptz, $7::jsonb
		ON CONFLICT (source_key) DO NOTHING
		RETURNING id, source_key, COALESCE(account_id, ''), COALESCE(device_id::text, ''), '', event_type, event_time, payload,
		          customer_id::text, seq`, sourceKey, customerID, accountID, deviceID, eventType, at, payload).
		Scan(&e.ID, &e.SourceKey, &e.AccountID, &e.DeviceID, &e.ServiceID, &e.EventType, &e.EventTime, &e.Payload, &e.CustomerID, &e.Seq)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("create customer event: %w", err)
	}
	return &e, nil
}
