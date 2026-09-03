package tenancy

import (
	"context"
	"fmt"
)

const (
	ScopeRegion   = "region"
	ScopeCustomer = "customer"
)

type Scope struct {
	Type string
	ID   string
}

// OperatorScopes returns every scope row assigned to an operator (empty
// slice, not an error, if none — meaning unrestricted, the default for
// every operator until a superadmin explicitly assigns scopes).
func (r *Repository) OperatorScopes(ctx context.Context, operatorID string) ([]Scope, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT scope_type, scope_id FROM operator_scopes WHERE operator_id = $1`, operatorID)
	if err != nil {
		return nil, fmt.Errorf("list operator scopes: %w", err)
	}
	defer rows.Close()
	var out []Scope
	for rows.Next() {
		var s Scope
		if err := rows.Scan(&s.Type, &s.ID); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *Repository) SetOperatorScopes(ctx context.Context, operatorID string, scopes []Scope) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin set operator scopes tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM operator_scopes WHERE operator_id = $1`, operatorID); err != nil {
		return fmt.Errorf("clear operator scopes: %w", err)
	}
	for _, s := range scopes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO operator_scopes (operator_id, scope_type, scope_id) VALUES ($1, $2, $3)`, operatorID, s.Type, s.ID); err != nil {
			return fmt.Errorf("insert operator scope: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit set operator scopes tx: %w", err)
	}
	return nil
}

// AccessibleCustomerIDs resolves an operator's scope rows into the
// concrete set of customer IDs they can see — a region scope expands to
// every customer currently under it (resolved live, not denormalized, so
// adding a customer to a scoped region immediately includes it).
//
// Zero scope rows resolves to scoped=true with an empty ID list: zero
// access (audit P0.1). Unrestricted access is never inferred from an
// empty scope result — it requires the caller to check the operator's
// superadmin role or explicit GlobalAccess grant *before* calling this
// function (see cmd/api's deviceScope), the same way the two are already
// short-circuited ahead of this call.
func (r *Repository) AccessibleCustomerIDs(ctx context.Context, operatorID string) (ids []string, scoped bool, err error) {
	scopes, err := r.OperatorScopes(ctx, operatorID)
	if err != nil {
		return nil, false, err
	}

	seen := map[string]bool{}
	for _, s := range scopes {
		switch s.Type {
		case ScopeCustomer:
			if !seen[s.ID] {
				seen[s.ID] = true
				ids = append(ids, s.ID)
			}
		case ScopeRegion:
			rows, err := r.db.QueryContext(ctx, `SELECT id FROM customers WHERE region_id = $1`, s.ID)
			if err != nil {
				return nil, false, fmt.Errorf("resolve region scope: %w", err)
			}
			for rows.Next() {
				var cid string
				if err := rows.Scan(&cid); err != nil {
					rows.Close()
					return nil, false, err
				}
				if !seen[cid] {
					seen[cid] = true
					ids = append(ids, cid)
				}
			}
			rows.Close()
		}
	}
	if ids == nil {
		ids = []string{} // scoped=true with a genuinely empty resolved set — see this func's doc comment
	}
	return ids, true, nil
}
