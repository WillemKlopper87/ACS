package alerting

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type Repository struct{ db *sql.DB }

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

func (r *Repository) Create(ctx context.Context, p Policy) (*Policy, error) {
	id := uuid.New().String()
	priorities, err := json.Marshal(p.FaultPriorities)
	if err != nil {
		return nil, fmt.Errorf("marshal fault priorities: %w", err)
	}
	var out Policy
	err = r.db.QueryRowContext(ctx, `INSERT INTO alert_policies (id,name,scope,tenant_id,group_id,device_id,customer_tier,enabled,fault_priorities,offline_after_seconds) VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,'')::uuid,NULLIF($6,'')::uuid,NULLIF($7,''),$8,$9,$10) RETURNING id,name,scope,COALESCE(tenant_id,''),COALESCE(group_id::text,''),COALESCE(device_id::text,''),COALESCE(customer_tier,''),enabled,fault_priorities,offline_after_seconds`, id, p.Name, p.Scope, p.TenantID, p.GroupID, p.DeviceID, p.CustomerTier, p.Enabled, priorities, int(p.OfflineAfter/time.Second)).Scan(&out.ID, &out.Name, &out.Scope, &out.TenantID, &out.GroupID, &out.DeviceID, &out.CustomerTier, &out.Enabled, &priorities, &out.OfflineAfter)
	if err != nil {
		return nil, fmt.Errorf("create alert policy: %w", err)
	}
	if err := json.Unmarshal(priorities, &out.FaultPriorities); err != nil {
		return nil, err
	}
	out.OfflineAfter *= time.Second
	return &out, nil
}

func (r *Repository) List(ctx context.Context) ([]Policy, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,name,scope,COALESCE(tenant_id,''),COALESCE(group_id::text,''),COALESCE(device_id::text,''),COALESCE(customer_tier,''),enabled,fault_priorities,offline_after_seconds FROM alert_policies ORDER BY scope,id`)
	if err != nil {
		return nil, fmt.Errorf("list alert policies: %w", err)
	}
	defer rows.Close()
	var out []Policy
	for rows.Next() {
		var p Policy
		var raw []byte
		var seconds int
		if err := rows.Scan(&p.ID, &p.Name, &p.Scope, &p.TenantID, &p.GroupID, &p.DeviceID, &p.CustomerTier, &p.Enabled, &raw, &seconds); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &p.FaultPriorities); err != nil {
			return nil, err
		}
		p.OfflineAfter = time.Duration(seconds) * time.Second
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *Repository) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM alert_policies WHERE id=$1", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
