// Package firmware owns firmware image metadata and storage (design doc
// v3 §7.6/§9.4, build plan §4 Phase 4). Metadata lives in Postgres;
// binaries do not — v3 §9.4/§19.4 are explicit that firmware belongs
// outside the relational database, served over HTTP(S) for the CPE to
// pull.
//
// Storage here is local disk, not the S3/MinIO/CDN v3 actually specifies.
// That's a deliberate lab-scope substitution, not an oversight: the
// interesting, protocol-correctness part of Phase 4 is Download/
// TransferComplete and the AWAITING_TRANSFER_COMPLETE job state, not
// which object store sits behind the URL. A real deployment swaps
// Storage's implementation for an S3 client; nothing above it (the
// Repository, the job dispatch logic, the REST handlers) needs to change,
// since they only ever see a URL.
package firmware

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"time"
)

// Image is a row of firmware_images.
type Image struct {
	ID            string
	Vendor        string
	Model         string
	Version       string
	Channel       string
	Filename      string
	FileSizeBytes int64
	SHA256        string
	ContentType   string
	ReleasedAt    *time.Time
	CreatedAt     time.Time
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

const imageColumns = `id, vendor, model, version, channel, filename, file_size_bytes, sha256, content_type, released_at, created_at`

// Create inserts image metadata. img.ID must already be set — the caller
// generates it before writing the file to Storage, so the same ID names
// both the DB row and the on-disk file.
func (r *Repository) Create(ctx context.Context, img Image) (*Image, error) {
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO firmware_images (id, vendor, model, version, channel, filename, file_size_bytes, sha256, content_type, released_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
		RETURNING `+imageColumns,
		img.ID, img.Vendor, img.Model, img.Version, img.Channel, img.Filename, img.FileSizeBytes, img.SHA256, img.ContentType)
	return scanImage(row)
}

func (r *Repository) Get(ctx context.Context, id string) (*Image, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+imageColumns+` FROM firmware_images WHERE id = $1`, id)
	return scanImage(row)
}

// LatestVersions returns the most recently released version per
// vendor+model (key: "vendor|model"), across every channel — a
// simplification for the dashboard's "firmware upgrade available" widget
// (admin-platform backlog): a real rollout decision should still go
// through internal/rollout's canary/failure-rate machinery, this is only
// meant to flag "this device might be behind," not to drive an automatic
// upgrade. Falls back to created_at when released_at is unset (an image
// uploaded without a release date, e.g. still in testing).
func (r *Repository) LatestVersions(ctx context.Context) (map[string]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT ON (vendor, model) vendor, model, version
		FROM firmware_images
		ORDER BY vendor, model, COALESCE(released_at, created_at) DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list latest firmware versions: %w", err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var vendor, model, version string
		if err := rows.Scan(&vendor, &model, &version); err != nil {
			return nil, fmt.Errorf("scan latest firmware version: %w", err)
		}
		out[vendor+"|"+model] = version
	}
	return out, rows.Err()
}

func (r *Repository) List(ctx context.Context) ([]Image, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+imageColumns+` FROM firmware_images ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list firmware images: %w", err)
	}
	defer rows.Close()

	var out []Image
	for rows.Next() {
		img, err := scanImage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *img)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanImage(s scanner) (*Image, error) {
	var img Image
	var releasedAt sql.NullTime
	if err := s.Scan(&img.ID, &img.Vendor, &img.Model, &img.Version, &img.Channel,
		&img.Filename, &img.FileSizeBytes, &img.SHA256, &img.ContentType,
		&releasedAt, &img.CreatedAt); err != nil {
		return nil, fmt.Errorf("scan firmware image: %w", err)
	}
	if releasedAt.Valid {
		t := releasedAt.Time
		img.ReleasedAt = &t
	}
	return &img, nil
}

// HashReader wraps r so that reading it to completion also computes its
// SHA256 — used by the upload handler to hash while streaming to disk
// instead of buffering the whole image in memory first.
type HashReader struct {
	r    io.Reader
	hash hash.Hash
	n    int64
}

func NewHashReader(r io.Reader) *HashReader {
	h := sha256.New()
	return &HashReader{r: io.TeeReader(r, h), hash: h}
}

func (hr *HashReader) Read(p []byte) (int, error) {
	n, err := hr.r.Read(p)
	hr.n += int64(n)
	return n, err
}

// Sum returns the hex-encoded SHA256 of everything read so far — call
// only after reading to EOF.
func (hr *HashReader) Sum() string {
	return hex.EncodeToString(hr.hash.Sum(nil))
}

func (hr *HashReader) BytesRead() int64 {
	return hr.n
}
