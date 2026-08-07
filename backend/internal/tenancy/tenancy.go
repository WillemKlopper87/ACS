// Package tenancy owns the multi-tenancy data model (admin-platform
// backlog, migration 0033): regions, customers (ISPs), projects, device
// assignment, and operator scoping. Shape: single-owner hierarchy
// (region -> customer -> device) plus project as an independent
// cross-cutting tag — see the migration's doc comment for why.
package tenancy

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"

	"acs/internal/store"
)

type Region struct {
	ID        string
	Name      string
	CreatedAt string
}

type Customer struct {
	ID       string
	Name     string
	RegionID *string
}

type Project struct {
	ID          string
	Name        string
	Description string
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// --- regions -------------------------------------------------------------

func (r *Repository) CreateRegion(ctx context.Context, name string) (*Region, error) {
	id := uuid.New().String()
	row := r.db.QueryRowContext(ctx, `INSERT INTO regions (id, name) VALUES ($1, $2) RETURNING id, name, created_at::text`, id, name)
	var reg Region
	if err := row.Scan(&reg.ID, &reg.Name, &reg.CreatedAt); err != nil {
		return nil, fmt.Errorf("create region: %w", err)
	}
	return &reg, nil
}

func (r *Repository) ListRegions(ctx context.Context) ([]Region, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name, created_at::text FROM regions ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list regions: %w", err)
	}
	defer rows.Close()
	var out []Region
	for rows.Next() {
		var reg Region
		if err := rows.Scan(&reg.ID, &reg.Name, &reg.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, reg)
	}
	return out, rows.Err()
}

func (r *Repository) DeleteRegion(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM regions WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete region: %w", err)
	}
	return nil
}

// --- customers -------------------------------------------------------------

func (r *Repository) CreateCustomer(ctx context.Context, name string, regionID *string) (*Customer, error) {
	id := uuid.New().String()
	row := r.db.QueryRowContext(ctx, `INSERT INTO customers (id, name, region_id) VALUES ($1, $2, $3) RETURNING id, name, region_id`, id, name, regionID)
	var c Customer
	if err := row.Scan(&c.ID, &c.Name, &c.RegionID); err != nil {
		return nil, fmt.Errorf("create customer: %w", err)
	}
	return &c, nil
}

func (r *Repository) ListCustomers(ctx context.Context) ([]Customer, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name, region_id FROM customers ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list customers: %w", err)
	}
	defer rows.Close()
	var out []Customer
	for rows.Next() {
		var c Customer
		if err := rows.Scan(&c.ID, &c.Name, &c.RegionID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *Repository) DeleteCustomer(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM customers WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete customer: %w", err)
	}
	return nil
}

// --- projects -------------------------------------------------------------

func (r *Repository) CreateProject(ctx context.Context, name, description string) (*Project, error) {
	id := uuid.New().String()
	row := r.db.QueryRowContext(ctx, `INSERT INTO projects (id, name, description) VALUES ($1, $2, $3) RETURNING id, name, COALESCE(description, '')`, id, name, nullIfEmpty(description))
	var p Project
	if err := row.Scan(&p.ID, &p.Name, &p.Description); err != nil {
		return nil, fmt.Errorf("create project: %w", err)
	}
	return &p, nil
}

func (r *Repository) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name, COALESCE(description, '') FROM projects ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Description); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *Repository) DeleteProject(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM projects WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	return nil
}

// --- device assignment -------------------------------------------------------------

// AssignDeviceCustomer sets (or clears, if customerID is nil) a device's
// owning customer.
func (r *Repository) AssignDeviceCustomer(ctx context.Context, deviceID string, customerID *string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE devices SET customer_id = $2, last_updated_at = now() WHERE id = $1`, deviceID, customerID)
	if err != nil {
		return fmt.Errorf("assign device customer: %w", err)
	}
	return nil
}

func (r *Repository) SetDeviceProjects(ctx context.Context, deviceID string, projectIDs []string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin set device projects tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM device_projects WHERE device_id = $1`, deviceID); err != nil {
		return fmt.Errorf("clear device projects: %w", err)
	}
	for _, pid := range projectIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO device_projects (device_id, project_id) VALUES ($1, $2)`, deviceID, pid); err != nil {
			return fmt.Errorf("insert device project: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit set device projects tx: %w", err)
	}
	return nil
}

func (r *Repository) DeviceProjects(ctx context.Context, deviceID string) ([]Project, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT p.id, p.name, COALESCE(p.description, '') FROM projects p
		JOIN device_projects dp ON dp.project_id = p.id
		WHERE dp.device_id = $1 ORDER BY p.name`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("list device projects: %w", err)
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Description); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CountByRegion groups devices by region, transitively through their
// customer (admin-platform backlog: dashboard "group by" widget) — a
// device with no customer, or a customer with no region, both fall into
// "Unassigned". customerIDs/scoped mirror devices.ListParams' multi-tenancy
// scoping (same store.StringArray comparison-as-text convention).
func (r *Repository) CountByRegion(ctx context.Context, customerIDs []string, scoped bool) (map[string]int, error) {
	clause := ""
	var args []any
	if scoped {
		clause = "WHERE d.customer_id::text = ANY($1)"
		args = append(args, store.StringArray(customerIDs))
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT COALESCE(reg.name, 'Unassigned'), COUNT(*)
		FROM devices d
		LEFT JOIN customers c ON c.id = d.customer_id
		LEFT JOIN regions reg ON reg.id = c.region_id
		`+clause+`
		GROUP BY reg.name
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("count devices by region: %w", err)
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var name string
		var count int
		if err := rows.Scan(&name, &count); err != nil {
			return nil, err
		}
		out[name] = count
	}
	return out, rows.Err()
}

// CountByProject groups devices by project tag. A device tagged with
// several projects is counted once under each (matches the "tag" semantics
// project assignment already has, see device_projects) — "Untagged" covers
// devices with no project at all.
func (r *Repository) CountByProject(ctx context.Context, customerIDs []string, scoped bool) (map[string]int, error) {
	clause := ""
	var args []any
	if scoped {
		clause = "WHERE d.customer_id::text = ANY($1)"
		args = append(args, store.StringArray(customerIDs))
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT COALESCE(p.name, 'Untagged'), COUNT(*)
		FROM devices d
		LEFT JOIN device_projects dp ON dp.device_id = d.id
		LEFT JOIN projects p ON p.id = dp.project_id
		`+clause+`
		GROUP BY p.name
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("count devices by project: %w", err)
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var name string
		var count int
		if err := rows.Scan(&name, &count); err != nil {
			return nil, err
		}
		out[name] = count
	}
	return out, rows.Err()
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
