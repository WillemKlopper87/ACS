// Package lifecycle manages a sale after it is in service: ending the
// service (offboarding), swapping the device, and moving the sale to
// another site or service plan. Each change runs in one transaction
// across the sale (onboardings), the account mappings, the device and
// the subscriber profile, so a half-done offboarding cannot be left
// behind.
//
// Ending a service releases the device to the unassigned pool (no
// customer, site, projects, nickname or location) and ends every active
// account mapping of the device. An account with no device left has its
// subscriber profile (name, VIP) moved to account_profile_archive, which
// internal/retention purges after the archive period (POPIA: personal
// data kept no longer than needed).
package lifecycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"acs/internal/bss"
	"acs/internal/onboarding"

	"github.com/google/uuid"
)

var (
	// ErrNotInService: the sale is not in service, so there is nothing to
	// end, swap or move.
	ErrNotInService = errors.New("the sale is not in service")
	// ErrInvalid: a request value is not allowed (reason, serial, site,
	// plan).
	ErrInvalid = errors.New("invalid request")
	// ErrSerialInUse: the replacement serial already has an open sale.
	ErrSerialInUse = errors.New("the replacement serial already has an open sale")
)

func invalidf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Reasons a service ends; the same set as an account mapping's unassign
// reason (migration 0052), which it is recorded as.
var reasonText = map[string]string{
	bss.ReasonReturn:    "service ended: device returned",
	bss.ReasonRMA:       "service ended: device faulty (RMA)",
	bss.ReasonUpgrade:   "service ended: device replaced by a newer one",
	bss.ReasonMoved:     "service ended: moved to another account",
	bss.ReasonCorrected: "service ended: sold in error",
}

// ValidReason reports whether reason may end a service.
func ValidReason(reason string) bool { _, ok := reasonText[reason]; return ok }

// Service runs the lifecycle changes.
type Service struct{ db *sql.DB }

func New(db *sql.DB) *Service { return &Service{db: db} }

// EndRequest ends the service of an in-service sale.
type EndRequest struct {
	OnboardingID string
	Reason       string
	Actor        string
	// KeepProfiles leaves the subscriber profile of an account with no
	// device left in place instead of archiving it (a replacement is on
	// its way through another channel).
	KeepProfiles bool
}

// EndResult says what an ended service touched.
type EndResult struct {
	DeviceID string
	// Accounts whose mapping of the device ended.
	Accounts []string
	// Archived are the accounts whose subscriber profile was archived.
	Archived []string
	// Mapping is the device's ended gateway-first mapping (role and plan),
	// which a swap gives the replacement.
	MappingRole, MappingPlan string
}

// End ends an in-service sale (ENDED), its device's account mappings, and
// releases the device; see the package comment.
func (s *Service) End(ctx context.Context, req EndRequest) (*EndResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := end(ctx, tx, req)
	if err != nil {
		return nil, err
	}
	return res, tx.Commit()
}

func end(ctx context.Context, tx *sql.Tx, req EndRequest) (*EndResult, error) {
	text, ok := reasonText[req.Reason]
	if !ok {
		return nil, invalidf("reason must be one of return, rma, upgrade, moved, corrected")
	}
	var state string
	var deviceID sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT state, device_id::text FROM onboardings WHERE id = $1 FOR UPDATE`, req.OnboardingID).Scan(&state, &deviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, onboarding.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock sale: %w", err)
	}
	if state != onboarding.StateInService || !deviceID.Valid {
		return nil, ErrNotInService
	}
	res := &EndResult{DeviceID: deviceID.String}

	if _, err := tx.ExecContext(ctx, `UPDATE onboardings SET state = 'ENDED', state_code = NULL, state_reason = $2, ended_at = now(), updated_at = now()
		WHERE id = $1`, req.OnboardingID, text); err != nil {
		return nil, fmt.Errorf("end sale: %w", err)
	}

	rows, err := tx.QueryContext(ctx, `UPDATE account_device_mappings SET unassigned_at = now(), unassign_reason = $2, updated_at = now()
		WHERE device_id = $1 AND unassigned_at IS NULL
		RETURNING account_id, role, COALESCE(service_plan, '')`, res.DeviceID, req.Reason)
	if err != nil {
		return nil, fmt.Errorf("end mappings: %w", err)
	}
	seen := map[string]bool{}
	for rows.Next() {
		var account, role, plan string
		if err := rows.Scan(&account, &role, &plan); err != nil {
			rows.Close()
			return nil, err
		}
		if !seen[account] {
			seen[account] = true
			res.Accounts = append(res.Accounts, account)
		}
		if res.MappingRole == "" || role == bss.RoleGateway {
			res.MappingRole, res.MappingPlan = role, plan
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Released: nothing of the old customer stays on the unit. The sale
	// keeps its site and account as history.
	if _, err := tx.ExecContext(ctx, `UPDATE devices SET customer_id = NULL, site_id = NULL, label = NULL, location = NULL,
		latitude = NULL, longitude = NULL, last_updated_at = now() WHERE id = $1`, res.DeviceID); err != nil {
		return nil, fmt.Errorf("release device: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM device_projects WHERE device_id = $1`, res.DeviceID); err != nil {
		return nil, fmt.Errorf("release device projects: %w", err)
	}

	if req.KeepProfiles {
		return res, nil
	}
	for _, account := range res.Accounts {
		var remaining bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM account_device_mappings WHERE account_id = $1 AND unassigned_at IS NULL)`,
			account).Scan(&remaining); err != nil {
			return nil, err
		}
		if remaining {
			continue
		}
		archived, err := archiveProfile(ctx, tx, account, req.Actor, text)
		if err != nil {
			return nil, err
		}
		if archived {
			res.Archived = append(res.Archived, account)
		}
	}
	return res, nil
}

// archiveProfile moves an account's profile, if it holds anything, to the
// archive and removes it.
func archiveProfile(ctx context.Context, tx *sql.Tx, account, actor, reason string) (bool, error) {
	out, err := tx.ExecContext(ctx, `INSERT INTO account_profile_archive (account_id, display_name, name_source, vip, reason, archived_by)
		SELECT account_id, display_name, name_source, vip, $2, $3 FROM account_profiles
		 WHERE account_id = $1 AND (display_name IS NOT NULL OR vip)`, account, reason, actor)
	if err != nil {
		return false, fmt.Errorf("archive subscriber profile: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM account_profiles WHERE account_id = $1`, account); err != nil {
		return false, fmt.Errorf("remove subscriber profile: %w", err)
	}
	n, _ := out.RowsAffected()
	return n > 0, nil
}

// SwapRequest replaces the device of an in-service sale.
type SwapRequest struct {
	OnboardingID string
	// Reason is rma or upgrade.
	Reason    string
	NewSerial string
	NewOUI    string
	Actor     string
}

// Swap ends the sale's service without archiving the subscriber (the
// account keeps going) and records a PLANNED sale for the replacement with
// the same customer, account, site and plan. When the old device had an
// account mapping, the replacement's sale carries its role and plan, so
// cmd/bssadapter maps the new device to the account when it connects.
func (s *Service) Swap(ctx context.Context, req SwapRequest) (*EndResult, string, error) {
	if req.Reason != bss.ReasonRMA && req.Reason != bss.ReasonUpgrade {
		return nil, "", invalidf("a swap's reason is rma or upgrade")
	}
	serial := strings.TrimSpace(req.NewSerial)
	if serial == "" {
		return nil, "", invalidf("the replacement's serial is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	ended, err := end(ctx, tx, EndRequest{OnboardingID: req.OnboardingID, Reason: req.Reason, Actor: req.Actor, KeepProfiles: true})
	if err != nil {
		return nil, "", err
	}
	newID := uuid.NewString()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO onboardings (id, customer_id, expected_oui, expected_serial, account_ref, site_id, service_plan_id, role,
			commercial_model, source, state, requested_by, bss_role, bss_service_plan)
		SELECT $2, customer_id, NULLIF($3, ''), $4, account_ref, site_id, service_plan_id, role,
			commercial_model, source, 'PLANNED', $5, NULLIF($6, ''), NULLIF($7, '')
		  FROM onboardings WHERE id = $1`,
		req.OnboardingID, newID, strings.ToUpper(strings.TrimSpace(req.NewOUI)), serial, req.Actor, ended.MappingRole, ended.MappingPlan)
	if err != nil && strings.Contains(err.Error(), "onboardings_open_serial_idx") {
		return nil, "", ErrSerialInUse
	}
	if err != nil {
		return nil, "", fmt.Errorf("record the replacement sale: %w", err)
	}
	return ended, newID, tx.Commit()
}

// MoveSite gives an in-service sale and its device another site.
func (s *Service) MoveSite(ctx context.Context, onboardingID, siteID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var deviceID string
	err = tx.QueryRowContext(ctx, `UPDATE onboardings o SET site_id = $2, updated_at = now()
		WHERE o.id = $1 AND o.state = 'IN_SERVICE' AND o.device_id IS NOT NULL
		  AND EXISTS (SELECT 1 FROM sites s WHERE s.id = $2 AND (s.customer_id IS NULL OR s.customer_id = o.customer_id))
		RETURNING o.device_id::text`, onboardingID, siteID).Scan(&deviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return s.why(ctx, tx, onboardingID, "site")
	}
	if err != nil {
		return fmt.Errorf("move sale: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE devices SET site_id = $2, last_updated_at = now() WHERE id = $1`, deviceID, siteID); err != nil {
		return fmt.Errorf("move device: %w", err)
	}
	return tx.Commit()
}

// ChangePlan gives an in-service sale another service plan. A plan that
// writes configuration sends the sale back to DISCOVERED, so the gateway
// configures the device on its next Inform and returns it to service; a
// plan with nothing to write takes effect at once.
func (s *Service) ChangePlan(ctx context.Context, onboardingID, planID string, configures bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE onboardings o SET service_plan_id = $2,
			state = CASE WHEN $3 THEN 'DISCOVERED' ELSE o.state END,
			state_reason = CASE WHEN $3 THEN 'service plan changed; configuring on the next check-in' ELSE o.state_reason END,
			state_code = NULL, config_jobs = '{}', updated_at = now()
		WHERE o.id = $1 AND o.state = 'IN_SERVICE'
		  AND EXISTS (SELECT 1 FROM service_plans p WHERE p.id = $2 AND (p.customer_id IS NULL OR p.customer_id = o.customer_id))`,
		onboardingID, planID, configures)
	if err != nil {
		return fmt.Errorf("change plan: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return s.why(ctx, tx, onboardingID, "service plan")
	}
	return tx.Commit()
}

// why explains a refused move: the sale is missing, not in service, or
// the site or plan is not one the sale's customer may use.
func (s *Service) why(ctx context.Context, tx *sql.Tx, onboardingID, what string) error {
	var state string
	err := tx.QueryRowContext(ctx, `SELECT state FROM onboardings WHERE id = $1`, onboardingID).Scan(&state)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return onboarding.ErrNotFound
	case err != nil:
		return err
	case state != onboarding.StateInService:
		return ErrNotInService
	}
	return invalidf("that %s is not one this sale's customer can use", what)
}
