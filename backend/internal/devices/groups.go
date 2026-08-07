// Device groups (build plan §4 Phase 7 / design doc v3 Phase 7: "Device
// groups"). A named, curated set of devices an operator builds up over
// time — the thing Fleet Control's bulk actions can target by group_id
// instead of re-selecting devices on every call. Deliberately its own
// repository/type (GroupRepository, DeviceGroup) rather than folded into
// Repository/Device — groups aren't a property of a device, they're a
// separate join, and errors.Is(ErrGroupNotFound) is cleaner against its
// own type than overloading sql.ErrNoRows across two different entities.
package devices

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrGroupNotFound = errors.New("device group not found")
	ErrGroupNameUsed = errors.New("a device group with that name already exists")
)

// DeviceGroup is a row of the device_groups table.
type DeviceGroup struct {
	ID          string
	Name        string
	Description string
	MemberCount int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type GroupRepository struct {
	db *sql.DB
}

func NewGroupRepository(db *sql.DB) *GroupRepository {
	return &GroupRepository{db: db}
}

// Create makes a new, empty group.
func (r *GroupRepository) Create(ctx context.Context, name, description string) (*DeviceGroup, error) {
	id := uuid.New().String()
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO device_groups (id, name, description)
		VALUES ($1, $2, $3)
		RETURNING id, name, description, created_at, updated_at`,
		id, name, description)

	var g DeviceGroup
	if err := row.Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt, &g.UpdatedAt); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrGroupNameUsed
		}
		return nil, fmt.Errorf("create device group: %w", err)
	}
	return &g, nil
}

// List returns every group with its current member count.
func (r *GroupRepository) List(ctx context.Context) ([]DeviceGroup, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT g.id, g.name, g.description, g.created_at, g.updated_at,
			COUNT(m.device_id)
		FROM device_groups g
		LEFT JOIN device_group_members m ON m.group_id = g.id
		GROUP BY g.id
		ORDER BY g.name`)
	if err != nil {
		return nil, fmt.Errorf("list device groups: %w", err)
	}
	defer rows.Close()

	var out []DeviceGroup
	for rows.Next() {
		var g DeviceGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt, &g.UpdatedAt, &g.MemberCount); err != nil {
			return nil, fmt.Errorf("scan device group: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Get fetches one group by ID, with its member count.
func (r *GroupRepository) Get(ctx context.Context, id string) (*DeviceGroup, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT g.id, g.name, g.description, g.created_at, g.updated_at,
			COUNT(m.device_id)
		FROM device_groups g
		LEFT JOIN device_group_members m ON m.group_id = g.id
		WHERE g.id = $1
		GROUP BY g.id`, id)

	var g DeviceGroup
	if err := row.Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt, &g.UpdatedAt, &g.MemberCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrGroupNotFound
		}
		return nil, fmt.Errorf("get device group: %w", err)
	}
	return &g, nil
}

// Delete removes a group. Membership rows go with it (ON DELETE CASCADE)
// — deleting a group never deletes the devices themselves.
func (r *GroupRepository) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM device_groups WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete device group: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrGroupNotFound
	}
	return nil
}

// AddMembers adds devices to a group, idempotently — re-adding an
// existing member is a no-op, not an error, since a bulk "add these N
// devices" call shouldn't fail just because one was already in the group.
func (r *GroupRepository) AddMembers(ctx context.Context, groupID string, deviceIDs []string) error {
	if len(deviceIDs) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin add members tx: %w", err)
	}
	defer tx.Rollback()

	for _, deviceID := range deviceIDs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO device_group_members (group_id, device_id)
			VALUES ($1, $2)
			ON CONFLICT (group_id, device_id) DO NOTHING`, groupID, deviceID); err != nil {
			return fmt.Errorf("add member %s: %w", deviceID, err)
		}
	}
	return tx.Commit()
}

// RemoveMember drops one device from a group.
func (r *GroupRepository) RemoveMember(ctx context.Context, groupID, deviceID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM device_group_members WHERE group_id = $1 AND device_id = $2`, groupID, deviceID)
	if err != nil {
		return fmt.Errorf("remove device group member: %w", err)
	}
	return nil
}

// MemberDeviceIDs resolves a group to its current device IDs — what
// cmd/api's bulk-action endpoint calls when a request targets a group_id
// instead of an explicit device_ids list.
func (r *GroupRepository) MemberDeviceIDs(ctx context.Context, groupID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT device_id FROM device_group_members WHERE group_id = $1`, groupID)
	if err != nil {
		return nil, fmt.Errorf("list group member device ids: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan group member device id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
