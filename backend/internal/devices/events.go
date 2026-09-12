package devices

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// eventsHistoryLimit caps how many rows Events returns when the caller
// doesn't otherwise bound it -- mirrors parameters.Repository.History's own
// historyLimit convention (a runaway device_events stream for one device
// should not be able to make this query unbounded).
const eventsHistoryLimit = 200

// DeviceEvent is one row of device_events (migration 0055) -- the
// per-device event stream that USP ObjectCreation, ObjectDeletion, and
// Event notifications land in.
type DeviceEvent struct {
	ID         int64
	DeviceID   string
	MsgID      string
	ObjPath    string
	EventName  string
	Params     map[string]string
	RecordedAt time.Time
}

// RecordEvent appends one event to a device's event stream.
//
// ON CONFLICT (device_id, msg_id) DO NOTHING is device_events' idempotency
// mechanism against USP's at-least-once Notify delivery: an agent can (and
// will) redeliver the same msg_id after a dropped ack, and that redelivery
// must not create a second row. This is a per-delivery-attempt dedup keyed
// on the transport-level msg_id, not a per-fact dedup -- two distinct
// events that happen to carry the same obj_path/event_name/params are two
// separate rows if they arrived with different msg_ids.
func (r *Repository) RecordEvent(ctx context.Context, deviceID, msgID, objPath, eventName string, params map[string]string) error {
	if params == nil {
		params = map[string]string{}
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal device event params: %w", err)
	}

	_, err = r.db.ExecContext(ctx, `
		INSERT INTO device_events (device_id, msg_id, obj_path, event_name, params)
		VALUES ($1, $2, $3, $4, $5::jsonb)
		ON CONFLICT (device_id, msg_id) DO NOTHING
	`, deviceID, msgID, objPath, eventName, encoded)
	if err != nil {
		return fmt.Errorf("record device event: %w", err)
	}
	return nil
}

// Events returns a device's recorded events, most recent first, capped at
// limit (or eventsHistoryLimit if limit is not positive) -- matching
// parameters.Repository.History's own ordering/limit convention.
func (r *Repository) Events(ctx context.Context, deviceID string, limit int) ([]DeviceEvent, error) {
	if limit <= 0 {
		limit = eventsHistoryLimit
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT id, device_id, msg_id, obj_path, event_name, params, recorded_at
		FROM device_events
		WHERE device_id = $1
		ORDER BY recorded_at DESC
		LIMIT $2`, deviceID, limit)
	if err != nil {
		return nil, fmt.Errorf("query device events: %w", err)
	}
	defer rows.Close()

	var out []DeviceEvent
	for rows.Next() {
		var e DeviceEvent
		var raw []byte
		if err := rows.Scan(&e.ID, &e.DeviceID, &e.MsgID, &e.ObjPath, &e.EventName, &raw, &e.RecordedAt); err != nil {
			return nil, fmt.Errorf("scan device event: %w", err)
		}
		e.Params = map[string]string{}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &e.Params); err != nil {
				return nil, fmt.Errorf("unmarshal device event params: %w", err)
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
