// Package bulkops records a fan-out of one action to many devices as a
// single bulk operation, so its progress can be read, its queued remainder
// cancelled and its failures retried after the request that started it has
// returned (docs/DEVICE-MANAGEMENT-PLAN.md).
//
// The operation does not own the work: each device still gets its own
// independent job. An item only points at the latest job for its device,
// and the operation's progress is those jobs' statuses read at query time.
package bulkops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"acs/internal/jobs"
	"acs/internal/store"
)

const (
	KindBulkAction    = "BULK_ACTION"
	KindTemplateApply = "TEMPLATE_APPLY"

	StateRunning  = "RUNNING"
	StateFinished = "FINISHED"

	defaultPageSize = 50
	maxPageSize     = 200
)

var ErrNotFound = errors.New("bulk operation not found")

// NewItem is one requested device's outcome at the moment of fan-out: the
// job that was queued for it, or the reason none was.
type NewItem struct {
	DeviceID   string
	CommandKey string
	Error      string
	Detail     string
}

// Counts is an operation's items by where their job stands.
type Counts struct {
	Total     int `json:"total"`
	Queued    int `json:"queued"`
	Running   int `json:"running"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
	// NotQueued: no job was created for the device (see the item's error).
	NotQueued int `json:"not_queued"`
	// Unknown: the job has since been pruned by retention.
	Unknown int `json:"unknown"`
}

// Operation is a bulk operation with its progress.
type Operation struct {
	ID        string
	Kind      string
	Action    string
	Label     string
	GroupID   *string
	CreatedBy string
	CreatedAt time.Time
	Counts    Counts
}

// State is RUNNING while any job is still queued or in flight.
func (o Operation) State() string {
	if o.Counts.Queued+o.Counts.Running > 0 {
		return StateRunning
	}
	return StateFinished
}

// Item is one device of an operation with its latest job.
type Item struct {
	Position     int
	DeviceID     string // as requested
	OUISerial    *string
	DeviceLabel  *string
	JobID        *string
	CommandKey   *string
	JobType      *string
	JobStatus    *string
	FaultString  *string
	Error        *string
	Detail       *string
	Retries      int
	jobRetryable bool
}

// Scope restricts reads and changes to the devices an operator may see.
// A scoped operator sees an operation only through its in-scope devices:
// the counts cover those items alone, and an operation with none is
// invisible.
type Scope struct {
	CustomerIDs []string
	Scoped      bool
}

type Repository struct {
	db   *sql.DB
	jobs *jobs.Repository
}

func NewRepository(db *sql.DB, jobRepo *jobs.Repository) *Repository {
	return &Repository{db: db, jobs: jobRepo}
}

// Create records an operation and its items. It is called after the
// fan-out, so a failure here loses only the record, never the jobs.
func (r *Repository) Create(ctx context.Context, kind, action, label, groupID, createdBy string, items []NewItem) (string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin bulk operation tx: %w", err)
	}
	defer tx.Rollback()

	var id string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO bulk_operations (kind, action, label, group_id, created_by)
		VALUES ($1, $2, $3, (SELECT id FROM device_groups WHERE id::text = $4), $5)
		RETURNING id`, kind, action, label, groupID, createdBy).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("insert bulk operation: %w", err)
	}

	deviceIDs := make([]string, len(items))
	commandKeys := make([]string, len(items))
	errs := make([]string, len(items))
	details := make([]string, len(items))
	for i, it := range items {
		deviceIDs[i], commandKeys[i], errs[i], details[i] = it.DeviceID, it.CommandKey, it.Error, it.Detail
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO bulk_operation_items
			(bulk_operation_id, position, requested_device_id, device_id, job_id, command_key, error, detail)
		SELECT $1, u.ord, u.device_id, d.id, j.id, NULLIF(u.command_key, ''), NULLIF(u.error, ''), NULLIF(u.detail, '')
		FROM unnest($2::text[], $3::text[], $4::text[], $5::text[])
			WITH ORDINALITY AS u(device_id, command_key, error, detail, ord)
		LEFT JOIN devices d ON d.id::text = u.device_id
		LEFT JOIN jobs j ON u.command_key <> '' AND j.command_key = u.command_key`,
		id, store.StringArray(deviceIDs), store.StringArray(commandKeys), store.StringArray(errs), store.StringArray(details))
	if err != nil {
		return "", fmt.Errorf("insert bulk operation items: %w", err)
	}
	return id, tx.Commit()
}

// scopeJoin is the join that limits items to in-scope devices; args is
// the scope's customer IDs as the next positional parameter, if scoped.
func scopeJoin(scope Scope, next int) (string, []any) {
	if !scope.Scoped {
		return "", nil
	}
	return fmt.Sprintf(` JOIN devices sd ON sd.id = i.device_id AND sd.customer_id::text = ANY($%d)`, next),
		[]any{store.StringArray(scope.CustomerIDs)}
}

const countColumns = `
	COUNT(i.position),
	COUNT(*) FILTER (WHERE j.status = 'QUEUED'),
	COUNT(*) FILTER (WHERE j.status IN ('RPC_SENT', 'IN_PROGRESS', 'AWAITING_TRANSFER_COMPLETE', 'AWAITING_DIAGNOSTICS_COMPLETE')),
	COUNT(*) FILTER (WHERE j.status = 'SUCCESS'),
	COUNT(*) FILTER (WHERE j.status IN ('FAILED', 'TIMEOUT')),
	COUNT(*) FILTER (WHERE j.status = 'CANCELLED'),
	COUNT(*) FILTER (WHERE i.position IS NOT NULL AND i.job_id IS NULL AND i.command_key IS NULL),
	COUNT(*) FILTER (WHERE i.position IS NOT NULL AND i.job_id IS NULL AND i.command_key IS NOT NULL)`

const operationColumns = `o.id, o.kind, o.action, o.label, o.group_id, o.created_by, o.created_at`

func scanOperation(s interface{ Scan(...any) error }, extra ...any) (*Operation, error) {
	var o Operation
	var groupID sql.NullString
	dest := []any{&o.ID, &o.Kind, &o.Action, &o.Label, &groupID, &o.CreatedBy, &o.CreatedAt,
		&o.Counts.Total, &o.Counts.Queued, &o.Counts.Running, &o.Counts.Succeeded,
		&o.Counts.Failed, &o.Counts.Cancelled, &o.Counts.NotQueued, &o.Counts.Unknown}
	if err := s.Scan(append(dest, extra...)...); err != nil {
		return nil, err
	}
	if groupID.Valid {
		o.GroupID = &groupID.String
	}
	return &o, nil
}

// List returns operations, newest first, and the number visible in scope.
func (r *Repository) List(ctx context.Context, scope Scope, page, pageSize int) ([]Operation, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	join, args := scopeJoin(scope, 3)
	// A scoped operator's inner join drops operations with no visible item;
	// an unscoped one keeps even an operation whose items are all gone.
	itemJoin := ` LEFT JOIN bulk_operation_items i ON i.bulk_operation_id = o.id`
	if scope.Scoped {
		itemJoin = ` JOIN bulk_operation_items i ON i.bulk_operation_id = o.id`
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+operationColumns+`, `+countColumns+`, COUNT(*) OVER ()
		FROM bulk_operations o`+itemJoin+join+`
		LEFT JOIN jobs j ON j.id = i.job_id
		GROUP BY o.id
		ORDER BY o.created_at DESC, o.id
		LIMIT $1 OFFSET $2`, append([]any{pageSize, (page - 1) * pageSize}, args...)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list bulk operations: %w", err)
	}
	defer rows.Close()

	out := []Operation{}
	total := 0
	for rows.Next() {
		o, err := scanOperation(rows, &total)
		if err != nil {
			return nil, 0, fmt.Errorf("scan bulk operation: %w", err)
		}
		out = append(out, *o)
	}
	return out, total, rows.Err()
}

// Get returns one operation with its items, or ErrNotFound when it does
// not exist or has no item in scope.
func (r *Repository) Get(ctx context.Context, id string, scope Scope) (*Operation, []Item, error) {
	join, args := scopeJoin(scope, 2)
	itemJoin := ` LEFT JOIN bulk_operation_items i ON i.bulk_operation_id = o.id`
	if scope.Scoped {
		itemJoin = ` JOIN bulk_operation_items i ON i.bulk_operation_id = o.id`
	}
	op, err := scanOperation(r.db.QueryRowContext(ctx, `
		SELECT `+operationColumns+`, `+countColumns+`
		FROM bulk_operations o`+itemJoin+join+`
		LEFT JOIN jobs j ON j.id = i.job_id
		WHERE o.id::text = $1
		GROUP BY o.id`, append([]any{id}, args...)...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("get bulk operation: %w", err)
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT i.position, i.requested_device_id, d.oui_serial, d.label, i.job_id, i.command_key,
			j.type, j.status, j.fault_string, i.error, i.detail, i.retries
		FROM bulk_operation_items i`+join+`
		LEFT JOIN devices d ON d.id = i.device_id
		LEFT JOIN jobs j ON j.id = i.job_id
		WHERE i.bulk_operation_id::text = $1
		ORDER BY i.position`, append([]any{id}, args...)...)
	if err != nil {
		return nil, nil, fmt.Errorf("list bulk operation items: %w", err)
	}
	defer rows.Close()

	items := []Item{}
	for rows.Next() {
		var it Item
		var ouiSerial, label, jobID, commandKey, jobType, jobStatus, fault, itemErr, detail sql.NullString
		if err := rows.Scan(&it.Position, &it.DeviceID, &ouiSerial, &label, &jobID, &commandKey,
			&jobType, &jobStatus, &fault, &itemErr, &detail, &it.Retries); err != nil {
			return nil, nil, fmt.Errorf("scan bulk operation item: %w", err)
		}
		it.OUISerial, it.DeviceLabel = strPtr(ouiSerial), strPtr(label)
		it.JobID, it.CommandKey = strPtr(jobID), strPtr(commandKey)
		it.JobType, it.JobStatus, it.FaultString = strPtr(jobType), strPtr(jobStatus), strPtr(fault)
		it.Error, it.Detail = strPtr(itemErr), strPtr(detail)
		// Cancelled jobs are left out: someone withdrew those on purpose.
		it.jobRetryable = jobStatus.Valid && (jobStatus.String == jobs.StatusFailed || jobStatus.String == jobs.StatusTimeout)
		items = append(items, it)
	}
	return op, items, rows.Err()
}

func strPtr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}

// CancelQueued cancels the operation's jobs that are still QUEUED and
// returns how many it cancelled.
func (r *Repository) CancelQueued(ctx context.Context, id string, scope Scope, by string) (int, error) {
	_, items, err := r.Get(ctx, id, scope)
	if err != nil {
		return 0, err
	}
	var ids []string
	for _, it := range items {
		if it.JobID != nil && it.JobStatus != nil && *it.JobStatus == jobs.StatusQueued {
			ids = append(ids, *it.JobID)
		}
	}
	return r.jobs.CancelMany(ctx, ids, by)
}

// RetryOutcome is what RetryFailed did.
type RetryOutcome struct {
	Retried int `json:"retried"`
	// Skipped maps a reason to the number of failed items left as they
	// were for it (a secret erased from the payload, for example).
	Skipped map[string]int `json:"skipped"`
}

// RetryFailed queues a new job for every item whose latest job ended
// FAILED or TIMEOUT, and points the item at it. A cancelled job is not
// retried (it was withdrawn on purpose; retry it from the Jobs screen),
// nor is an item that never had a job: the reason it was not queued (an
// unwritable parameter, an unknown device) still holds.
func (r *Repository) RetryFailed(ctx context.Context, id string, scope Scope, by string) (RetryOutcome, error) {
	out := RetryOutcome{Skipped: map[string]int{}}
	_, items, err := r.Get(ctx, id, scope)
	if err != nil {
		return out, err
	}
	for _, it := range items {
		if !it.jobRetryable || it.JobID == nil {
			continue
		}
		retried, reason, err := r.retryItem(ctx, id, it, by)
		if err != nil {
			return out, err
		}
		if retried {
			out.Retried++
		} else if reason != "" {
			out.Skipped[reason]++
		}
	}
	return out, nil
}

// retryItem retries one item under a row lock, so two retries of the same
// operation at once cannot each queue a job for the same device: the
// second waits, then finds the item already pointing at a newer job.
func (r *Repository) retryItem(ctx context.Context, id string, it Item, by string) (retried bool, skipReason string, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, "", fmt.Errorf("begin retry tx: %w", err)
	}
	defer tx.Rollback()

	var current sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT job_id FROM bulk_operation_items
		WHERE bulk_operation_id::text = $1 AND position = $2 FOR UPDATE`, id, it.Position).Scan(&current)
	if err != nil {
		return false, "", fmt.Errorf("lock bulk operation item: %w", err)
	}
	if !current.Valid || current.String != *it.JobID {
		return false, "", nil // already retried by someone else
	}
	job, err := r.jobs.Retry(ctx, *it.JobID, by)
	if err != nil {
		return false, err.Error(), nil
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE bulk_operation_items SET job_id = $1, command_key = $2, retries = retries + 1
		WHERE bulk_operation_id::text = $3 AND position = $4`, job.ID, job.CommandKey, id, it.Position); err != nil {
		return false, "", fmt.Errorf("update bulk operation item: %w", err)
	}
	return true, "", tx.Commit()
}
