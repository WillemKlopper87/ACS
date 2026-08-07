package cliaccess

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// WebGUIConfig is a device's own admin-UI base URL plus optional HTTP
// Basic Auth, for the console panel's proxied-iframe embed. Same CGNAT
// reachability constraint as SSH/Telnet (package doc comment) — the ACS
// has to be able to reach base_url for the proxy in cmd/api to work.
type WebGUIConfig struct {
	DeviceID  string
	BaseURL   string
	Username  string
	Password  string
	UpdatedAt time.Time
}

// SetWebGUIConfig replaces a device's web-GUI config wholesale — see
// 0031_device_webgui_config.sql for why this is a single overwritten row
// rather than a versioned table like device_cli_credentials.
func (r *Repository) SetWebGUIConfig(ctx context.Context, deviceID, baseURL, username, password string) (*WebGUIConfig, error) {
	encryptedPassword, err := r.encrypt(password)
	if err != nil {
		return nil, err
	}
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO device_webgui_config (device_id, base_url, username, password, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (device_id) DO UPDATE SET
			base_url = EXCLUDED.base_url, username = EXCLUDED.username, password = EXCLUDED.password, updated_at = now()
		RETURNING device_id, base_url, COALESCE(username, ''), COALESCE(password, ''), updated_at
	`, deviceID, baseURL, nullIfEmpty(username), nullIfEmpty(encryptedPassword))
	return r.scanWebGUIConfig(row)
}

// GetWebGUIConfig returns a device's web-GUI config, or nil if none has
// been set — cmd/api's proxy handler treats that as "not configured", not
// an error.
func (r *Repository) GetWebGUIConfig(ctx context.Context, deviceID string) (*WebGUIConfig, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT device_id, base_url, COALESCE(username, ''), COALESCE(password, ''), updated_at
		FROM device_webgui_config WHERE device_id = $1`, deviceID)
	cfg, err := r.scanWebGUIConfig(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return cfg, err
}

func (r *Repository) DeleteWebGUIConfig(ctx context.Context, deviceID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM device_webgui_config WHERE device_id = $1`, deviceID)
	if err != nil {
		return fmt.Errorf("delete webgui config: %w", err)
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

type webGUIScanner interface {
	Scan(dest ...any) error
}

func (r *Repository) scanWebGUIConfig(s webGUIScanner) (*WebGUIConfig, error) {
	var c WebGUIConfig
	if err := s.Scan(&c.DeviceID, &c.BaseURL, &c.Username, &c.Password, &c.UpdatedAt); err != nil {
		return nil, fmt.Errorf("scan webgui config: %w", err)
	}
	plaintext, err := r.decrypt(c.Password)
	if err != nil {
		return nil, fmt.Errorf("webgui config %s: %w", c.DeviceID, err)
	}
	c.Password = plaintext
	return &c, nil
}
