// Package bulkdata stores bounded BBF JSON NameValuePair reports separately
// from operational logs and TR-143 throughput diagnostic results.
package bulkdata

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"strings"
	"time"
)

type Receiver struct {
	ID              string    `json:"id"`
	DeviceID        string    `json:"device_id"`
	ProfileInstance int       `json:"profile_instance"`
	Paths           []string  `json:"paths"`
	ExpiresAt       time.Time `json:"expires_at"`
	Revoked         bool      `json:"revoked"`
}
type Report struct {
	ID         int64           `json:"id"`
	Payload    json.RawMessage `json:"report"`
	ReceivedAt time.Time       `json:"received_at"`
}
type Repository struct{ db *sql.DB }

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

func (r *Repository) Create(ctx context.Context, deviceID string, instance int, paths []string, expires time.Time, interval ...int) (Receiver, error) {
	s := Receiver{ID: uuid.NewString(), DeviceID: deviceID, ProfileInstance: instance, Paths: paths, ExpiresAt: expires}
	raw, err := json.Marshal(paths)
	if err != nil {
		return s, err
	}
	seconds := 300
	if len(interval) > 0 {
		seconds = interval[0]
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO bulk_data_receivers(id,device_id,profile_instance,parameter_paths,expires_at,reporting_interval) VALUES($1,$2,$3,$4,$5,$6)`, s.ID, deviceID, instance, raw, expires, seconds)
	return s, err
}
func (r *Repository) Get(ctx context.Context, id string) (Receiver, error) {
	var s Receiver
	var paths []byte
	err := r.db.QueryRowContext(ctx, `SELECT id,device_id,profile_instance,parameter_paths,expires_at,revoked FROM bulk_data_receivers WHERE id=$1`, id).Scan(&s.ID, &s.DeviceID, &s.ProfileInstance, &paths, &s.ExpiresAt, &s.Revoked)
	if err == nil {
		err = json.Unmarshal(paths, &s.Paths)
	}
	return s, err
}
func (r *Repository) Revoke(ctx context.Context, id, deviceID string) error {
	result, err := r.db.ExecContext(ctx, `UPDATE bulk_data_receivers SET revoked=true WHERE id=$1 AND device_id=$2`, id, deviceID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ValidateReport supports the flat NameValuePair format we configure on CPEs.
// ObjectHierarchy, CSV and MQTT collection are explicit unsupported formats.
func ValidateReport(raw []byte, allowed []string) error {
	var envelope struct {
		Reports []map[string]json.RawMessage `json:"Report"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("invalid JSON report")
	}
	if len(envelope.Reports) == 0 || len(envelope.Reports) > 32 {
		return fmt.Errorf("Report must contain 1 to 32 collections")
	}
	paths := map[string]bool{}
	for _, path := range allowed {
		paths[path] = true
	}
	count := 0
	for _, collection := range envelope.Reports {
		metrics := 0
		for path, value := range collection {
			if path == "CollectionTime" {
				var stamp any
				if json.Unmarshal(value, &stamp) != nil {
					return fmt.Errorf("invalid CollectionTime")
				}
				switch stamp.(type) {
				case string, float64:
				default:
					return fmt.Errorf("CollectionTime must be a string or number")
				}
				continue
			}
			if !paths[path] {
				return fmt.Errorf("report contains a parameter outside the configured profile")
			}
			var scalar any
			if json.Unmarshal(value, &scalar) != nil {
				return fmt.Errorf("invalid value")
			}
			switch scalar.(type) {
			case string, float64, bool, nil:
			default:
				return fmt.Errorf("NameValuePair values must be scalar")
			}
			metrics++
			count++
		}
		if metrics == 0 {
			return fmt.Errorf("collection contains no configured metrics")
		}
	}
	if count > 10000 {
		return fmt.Errorf("report exceeds 10000 metrics")
	}
	return nil
}

func (r *Repository) SaveReport(ctx context.Context, s Receiver, raw []byte) (bool, error) {
	if err := ValidateReport(raw, s.Paths); err != nil {
		return false, err
	}
	hash := sha256.Sum256(raw)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT NOT revoked AND expires_at>now() FROM bulk_data_receivers WHERE id=$1 FOR UPDATE`, s.ID).Scan(&active); err != nil {
		return false, err
	}
	if !active {
		return false, fmt.Errorf("receiver expired or revoked")
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO bulk_data_reports(receiver_id,sha256,report) VALUES($1,$2,$3) ON CONFLICT(receiver_id,sha256) DO NOTHING`, s.ID, hex.EncodeToString(hash[:]), raw)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	// At most 100 bounded reports per receiver. No unbounded raw log archive.
	if _, err := tx.ExecContext(ctx, `UPDATE bulk_data_receivers SET last_report_at=now() WHERE id=$1`, s.ID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM bulk_data_reports WHERE receiver_id=$1 AND id NOT IN(SELECT id FROM bulk_data_reports WHERE receiver_id=$1 ORDER BY id DESC LIMIT 100)`, s.ID); err != nil {
		return false, err
	}
	return n == 0, tx.Commit()
}
func (r *Repository) Reports(ctx context.Context, deviceID string) ([]Report, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT p.id,p.report,p.received_at FROM bulk_data_reports p JOIN bulk_data_receivers r ON r.id=p.receiver_id WHERE r.device_id=$1 ORDER BY p.received_at DESC LIMIT 100`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Report{}
	for rows.Next() {
		var p Report
		if err := rows.Scan(&p.ID, &p.Payload, &p.ReceivedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func IsMetricPath(path string) bool {
	return strings.HasPrefix(path, "Device.") && !strings.HasSuffix(path, ".") && !strings.ContainsAny(path, "{}*[]()") && len(path) <= 1024
}
