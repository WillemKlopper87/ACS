package bss

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type ServiceOrder struct {
	ID          string
	ExternalID  string
	AccountID   string
	RawRequest  json.RawMessage
	CreatedAt   time.Time
	CancelledAt *time.Time
}

type ServiceOrderItem struct {
	ID              string
	OrderID         string
	Seq             int
	Action          string
	Role            string
	MappingID       string
	ExternalOrderID string
	Status          string
	LastError       string
	CompletedAt     *time.Time
}

var ErrServiceOrderExists = errors.New("service order already exists")

func (r *Repository) CreateServiceOrder(ctx context.Context, externalID, accountID string, raw json.RawMessage, items []ServiceOrderItem) (*ServiceOrder, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin service order: %w", err)
	}
	defer tx.Rollback()
	id := uuid.New().String()
	var order ServiceOrder
	err = tx.QueryRowContext(ctx, `INSERT INTO service_orders (id, external_id, account_id, raw_request) VALUES ($1,$2,$3,$4) RETURNING id, external_id, account_id, raw_request, created_at`, id, externalID, accountID, raw).Scan(&order.ID, &order.ExternalID, &order.AccountID, &order.RawRequest, &order.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrServiceOrderExists
		}
		return nil, fmt.Errorf("insert service order: %w", err)
	}
	for i := range items {
		item := &items[i]
		if item.ID == "" {
			item.ID = uuid.New().String()
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO service_order_items (id, order_id, seq, action, role, mapping_id, external_order_id, status, last_error, completed_at) VALUES ($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,'')::uuid,NULLIF($7,''),$8,NULLIF($9,''),$10)`, item.ID, id, item.Seq, item.Action, item.Role, item.MappingID, item.ExternalOrderID, item.Status, item.LastError, item.CompletedAt)
		if err != nil {
			return nil, fmt.Errorf("insert service order item: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit service order: %w", err)
	}
	return &order, nil
}

func (r *Repository) FindServiceOrder(ctx context.Context, externalID string) (*ServiceOrder, error) {
	var order ServiceOrder
	err := r.db.QueryRowContext(ctx, `SELECT id, external_id, account_id, raw_request, created_at, cancelled_at FROM service_orders WHERE external_id=$1`, externalID).Scan(&order.ID, &order.ExternalID, &order.AccountID, &order.RawRequest, &order.CreatedAt, &order.CancelledAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find service order: %w", err)
	}
	return &order, nil
}

func (r *Repository) ServiceOrderItems(ctx context.Context, orderID string) ([]ServiceOrderItem, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, order_id, seq, action, COALESCE(role,''), COALESCE(mapping_id::text,''), COALESCE(external_order_id,''), status, COALESCE(last_error,''), completed_at FROM service_order_items WHERE order_id=$1 ORDER BY seq`, orderID)
	if err != nil {
		return nil, fmt.Errorf("list service order items: %w", err)
	}
	defer rows.Close()
	var out []ServiceOrderItem
	for rows.Next() {
		var item ServiceOrderItem
		if err := rows.Scan(&item.ID, &item.OrderID, &item.Seq, &item.Action, &item.Role, &item.MappingID, &item.ExternalOrderID, &item.Status, &item.LastError, &item.CompletedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *Repository) CancelServiceOrder(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE service_orders SET cancelled_at=now() WHERE id=$1 AND cancelled_at IS NULL AND NOT EXISTS (SELECT 1 FROM service_order_items WHERE order_id=$1 AND status <> 'PENDING')`, id)
	if err != nil {
		return fmt.Errorf("cancel service order: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *Repository) ListServiceOrders(ctx context.Context, accountID string, limit int) ([]ServiceOrder, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id,external_id,account_id,raw_request,created_at,cancelled_at FROM service_orders WHERE ($1='' OR account_id=$1) ORDER BY created_at DESC LIMIT $2`, accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("list service orders: %w", err)
	}
	defer rows.Close()
	var out []ServiceOrder
	for rows.Next() {
		var o ServiceOrder
		if err := rows.Scan(&o.ID, &o.ExternalID, &o.AccountID, &o.RawRequest, &o.CreatedAt, &o.CancelledAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (r *Repository) UpdateServiceOrderItem(ctx context.Context, itemID, status, lastError string) error {
	var completed any
	if status == "COMPLETED" || status == "FAILED" || status == "SKIPPED" {
		completed = time.Now().UTC()
	}
	res, err := r.db.ExecContext(ctx, `UPDATE service_order_items SET status=$2,last_error=NULLIF($3,''),completed_at=$4 WHERE id=$1`, itemID, status, lastError, completed)
	if err != nil {
		return fmt.Errorf("update service order item: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
