// SSH host key pinning for the console bridge (audit P0.4). Keys are
// pinned per device on first successful connect (trust-on-first-use)
// and must match on every later connect — replacing the previous
// ssh.InsecureIgnoreHostKey(), which accepted any interceptor.
package cliaccess

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"

	"golang.org/x/crypto/ssh"
)

// ErrHostKeyMismatch means the device presented a different host key
// than the one pinned for it — either a machine-in-the-middle, or a
// legitimate key change (firmware reset). The remediation for the
// latter is an explicit re-enrollment: delete the pinned row
// (device_ssh_host_keys) after verifying the change out of band.
var ErrHostKeyMismatch = errors.New("ssh host key does not match the key pinned for this device")

// HostKey returns the pinned host key for a device ("" if none yet).
func (r *Repository) HostKey(ctx context.Context, deviceID string) (string, error) {
	var key string
	err := r.db.QueryRowContext(ctx, `SELECT host_key FROM device_ssh_host_keys WHERE device_id = $1`, deviceID).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load ssh host key: %w", err)
	}
	return key, nil
}

// RecordHostKey pins a host key for a device if none is pinned yet
// (first-use trust). It never overwrites an existing pin.
func (r *Repository) RecordHostKey(ctx context.Context, deviceID, key string) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO device_ssh_host_keys (device_id, host_key) VALUES ($1, $2)
		ON CONFLICT (device_id) DO NOTHING`, deviceID, key)
	if err != nil {
		return fmt.Errorf("record ssh host key: %w", err)
	}
	return nil
}

// encodeHostKey renders a public key in the single-line authorized_keys
// form ("ssh-ed25519 AAAA...").
func encodeHostKey(key ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

// TOFUHostKeyCallback builds the ssh.HostKeyCallback for one device:
// pin on first use, require an exact match after.
func (r *Repository) TOFUHostKeyCallback(ctx context.Context, deviceID string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		presented := encodeHostKey(key)
		pinned, err := r.HostKey(ctx, deviceID)
		if err != nil {
			return err
		}
		if pinned == "" {
			return r.RecordHostKey(ctx, deviceID, presented)
		}
		if pinned != presented {
			return fmt.Errorf("%w (device %s at %s presented %s)", ErrHostKeyMismatch, deviceID, hostname, key.Type())
		}
		return nil
	}
}
