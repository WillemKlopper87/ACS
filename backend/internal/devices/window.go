package devices

import (
	"context"
	"fmt"

	"acs/internal/store"
)

// WindowParams is the offset/limit counterpart to ListParams. TM Forum list
// APIs use offset/limit rather than page/page_size, so converting through a
// page number would skip or duplicate rows for offsets that are not exact page
// boundaries.
type WindowParams struct {
	Offset      int
	Limit       int
	CustomerIDs []string
	Scoped      bool
}

// ListWindow returns a deterministic offset/limit window over the existing
// device inventory while applying the exact same customer tenancy boundary as
// List. It does not create a second resource store for TMF639.
func (r *Repository) ListWindow(ctx context.Context, params WindowParams) (*ListResult, error) {
	offset := params.Offset
	if offset < 0 {
		offset = 0
	}
	limit := params.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}

	scopeClause := ""
	scopeArgs := []any{}
	if params.Scoped {
		scopeClause = "WHERE customer_id::text = ANY($1)"
		scopeArgs = append(scopeArgs, store.StringArray(params.CustomerIDs))
	}

	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices `+scopeClause, scopeArgs...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count devices window: %w", err)
	}

	args := append(append([]any{}, scopeArgs...), limit, offset)
	limitOffset := fmt.Sprintf("LIMIT $%d OFFSET $%d", len(scopeArgs)+1, len(scopeArgs)+2)
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+deviceColumns+` FROM devices `+scopeClause+`
		ORDER BY last_inform_at DESC NULLS LAST, id ASC
		`+limitOffset,
		args...)
	if err != nil {
		return nil, fmt.Errorf("list devices window: %w", err)
	}
	defer rows.Close()

	items := make([]Device, 0, limit)
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("finish devices window: %w", err)
	}
	return &ListResult{Items: items, Total: total}, nil
}
