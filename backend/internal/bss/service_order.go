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
	ClaimedAt       *time.Time
}

// ServiceOrderWork is an item claimed for execution, with the durable
// request needed to resume it after a process restart.
type ServiceOrderWork struct {
	Order ServiceOrder
	Item  ServiceOrderItem
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
	rows, err := r.db.QueryContext(ctx, `SELECT id, order_id, seq, action, COALESCE(role,''), COALESCE(mapping_id::text,''), COALESCE(external_order_id,''), status, COALESCE(last_error,''), completed_at, claimed_at FROM service_order_items WHERE order_id=$1 ORDER BY seq`, orderID)
	if err != nil {
		return nil, fmt.Errorf("list service order items: %w", err)
	}
	defer rows.Close()
	var out []ServiceOrderItem
	for rows.Next() {
		var item ServiceOrderItem
		if err := rows.Scan(&item.ID, &item.OrderID, &item.Seq, &item.Action, &item.Role, &item.MappingID, &item.ExternalOrderID, &item.Status, &item.LastError, &item.CompletedAt, &item.ClaimedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ClaimDueServiceOrderItems leases the oldest runnable item from each order.
// Earlier items must be terminal before the next item is claimed. Pending
// work is given a short age grace so the request handler can finish its
// synchronous pass; abandoned RUNNING leases are reclaimed after ten minutes.
func (r *Repository) ClaimDueServiceOrderItems(ctx context.Context, limit int) ([]ServiceOrderWork, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := r.db.QueryContext(ctx, `
		WITH candidates AS (
			SELECT i.id
			  FROM service_order_items i
			  JOIN service_orders o ON o.id = i.order_id
			 WHERE o.cancelled_at IS NULL
			   AND ((i.status = 'PENDING' AND o.created_at < now() - interval '30 seconds')
			     OR (i.status = 'RUNNING' AND i.claimed_at < now() - interval '10 minutes'))
			   AND NOT EXISTS (
				 SELECT 1 FROM service_order_items prior
				  WHERE prior.order_id = i.order_id AND prior.seq < i.seq
				    AND prior.status NOT IN ('COMPLETED', 'DISPATCHED')
			   )
			 ORDER BY o.created_at, i.seq
			 LIMIT $1
			 FOR UPDATE OF i SKIP LOCKED
		), claimed AS (
			UPDATE service_order_items i
			   SET status = 'RUNNING', claimed_at = now()
			  FROM candidates c WHERE i.id = c.id
			 RETURNING i.id, i.order_id, i.seq, i.action, COALESCE(i.role,'') AS role,
			           COALESCE(i.mapping_id::text,'') AS mapping_id, COALESCE(i.external_order_id,'') AS external_order_id,
			           i.status, COALESCE(i.last_error,'') AS last_error, i.completed_at, i.claimed_at
		)
		SELECT o.id, o.external_id, o.account_id, o.raw_request, o.created_at, o.cancelled_at,
		       c.id, c.order_id, c.seq, c.action, c.role, c.mapping_id,
		       c.external_order_id, c.status, c.last_error, c.completed_at, c.claimed_at
		  FROM claimed c JOIN service_orders o ON o.id = c.order_id
		 ORDER BY o.created_at, c.seq`, limit)
	if err != nil {
		return nil, fmt.Errorf("claim due TMF641 items: %w", err)
	}
	defer rows.Close()
	var work []ServiceOrderWork
	for rows.Next() {
		var w ServiceOrderWork
		if err := rows.Scan(&w.Order.ID, &w.Order.ExternalID, &w.Order.AccountID, &w.Order.RawRequest, &w.Order.CreatedAt, &w.Order.CancelledAt,
			&w.Item.ID, &w.Item.OrderID, &w.Item.Seq, &w.Item.Action, &w.Item.Role, &w.Item.MappingID,
			&w.Item.ExternalOrderID, &w.Item.Status, &w.Item.LastError, &w.Item.CompletedAt, &w.Item.ClaimedAt); err != nil {
			return nil, fmt.Errorf("scan claimed TMF641 item: %w", err)
		}
		work = append(work, w)
	}
	return work, rows.Err()
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
	res, err := r.db.ExecContext(ctx, `UPDATE service_order_items SET status=$2,last_error=NULLIF($3,''),completed_at=$4,claimed_at=NULL WHERE id=$1`, itemID, status, lastError, completed)
	if err != nil {
		return fmt.Errorf("update service order item: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *Repository) SetServiceOrderItemMapping(ctx context.Context, itemID, mappingID string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE service_order_items SET mapping_id=NULLIF($2,'')::uuid WHERE id=$1`, itemID, mappingID)
	if err != nil {
		return fmt.Errorf("set service order item mapping: %w", err)
	}
	return nil
}
