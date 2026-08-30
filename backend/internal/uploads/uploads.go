// Package uploads owns the CPE-to-ACS direction of file transfer (TR-069
// §A.3.2.7's Upload RPC — nice-to-have feature backlog) — the mirror
// image of internal/firmware's ACS-to-CPE Download. A CPE never sends the
// file inline in a SOAP call; it PUTs the bytes to a URL the Upload RPC
// gave it, independent of the CWMP session that dispatched the RPC, and
// the outcome shows up later as TransferComplete (the same RPC Download
// already uses — this package doesn't own that correlation, cmd/acs
// does).
package uploads

import (
	"acs/internal/objstore"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"time"
)

const (
	StatusPending  = "PENDING"
	StatusReceived = "RECEIVED"
)

var ErrNotFound = errors.New("uploaded file not found")

// UploadedFile is a row of the uploaded_files table.
type UploadedFile struct {
	ID            string
	DeviceID      string
	FileType      string
	Status        string
	Filename      *string
	FileSizeBytes *int64
	SHA256        *string
	CreatedBy     string
	CreatedAt     time.Time
	ReceivedAt    *time.Time
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

const columns = `id, device_id, file_type, status, filename, file_size_bytes, sha256, created_by, created_at, received_at`

// Create records a PENDING upload slot — id becomes the token embedded in
// the receipt URL the Upload RPC's payload carries, so the CPE's eventual
// PUT can be matched back to this row without any session state.
func (r *Repository) Create(ctx context.Context, deviceID, fileType, createdBy string) (*UploadedFile, error) {
	id := uuid.New().String()
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO uploaded_files (id, device_id, file_type, status, created_by)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+columns,
		id, deviceID, fileType, StatusPending, nullIfEmpty(createdBy))
	return scan(row)
}

// MarkReceived records that the file actually arrived — called by the
// upload-receipt HTTP handler once the CPE's PUT has streamed to disk.
// The status predicate makes the PENDING->RECEIVED transition atomic
// and single-use (audit P0.3): a second PUT racing on the same slot
// gets ErrNotFound instead of silently overwriting the recorded file.
func (r *Repository) MarkReceived(ctx context.Context, id, filename, sha256hex string, size int64) (*UploadedFile, error) {
	row := r.db.QueryRowContext(ctx, `
		UPDATE uploaded_files SET status = $2, filename = $3, file_size_bytes = $4, sha256 = $5, received_at = now()
		WHERE id = $1 AND status = $6
		RETURNING `+columns,
		id, StatusReceived, filename, size, sha256hex, StatusPending)
	f, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return f, err
}

func (r *Repository) ByID(ctx context.Context, id string) (*UploadedFile, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+columns+` FROM uploaded_files WHERE id = $1`, id)
	f, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return f, err
}

func (r *Repository) ListByDevice(ctx context.Context, deviceID string) ([]UploadedFile, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+columns+` FROM uploaded_files WHERE device_id = $1 ORDER BY created_at DESC`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("list uploaded files: %w", err)
	}
	defer rows.Close()

	var out []UploadedFile
	for rows.Next() {
		f, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner) (*UploadedFile, error) {
	var f UploadedFile
	var filename, sha256hex, createdBy sql.NullString
	var size sql.NullInt64
	var receivedAt sql.NullTime

	if err := s.Scan(&f.ID, &f.DeviceID, &f.FileType, &f.Status, &filename, &size, &sha256hex, &createdBy, &f.CreatedAt, &receivedAt); err != nil {
		return nil, fmt.Errorf("scan uploaded file: %w", err)
	}
	if filename.Valid {
		f.Filename = &filename.String
	}
	if size.Valid {
		f.FileSizeBytes = &size.Int64
	}
	if sha256hex.Valid {
		f.SHA256 = &sha256hex.String
	}
	if createdBy.Valid {
		f.CreatedBy = createdBy.String
	}
	if receivedAt.Valid {
		t := receivedAt.Time
		f.ReceivedAt = &t
	}
	return &f, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Storage is the uploaded-file object store — local disk by default, S3
// when ACS_OBJECT_STORE=s3 (see internal/objstore).
type Storage = objstore.Store

// NewStorage returns the local-disk backend rooted at root; main() uses
// objstore.FromEnv to pick the backend from the environment.
func NewStorage(root string) (Storage, error) {
	return objstore.NewLocal(root)
}
