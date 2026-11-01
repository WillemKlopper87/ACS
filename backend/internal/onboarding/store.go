package onboarding

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"acs/internal/store"
)

var (
	ErrNotFound = errors.New("onboarding: not found")
	// ErrOpenOnboardingExists: a serial has at most one open onboarding.
	ErrOpenOnboardingExists = errors.New("an open onboarding already exists for this serial number")
	// ErrInvalid marks input the caller must fix; the message says how.
	ErrInvalid = errors.New("onboarding: invalid input")
	// ErrExists: a site external reference or plan code is already taken.
	ErrExists = errors.New("onboarding: already exists")
	// ErrOtherTenant: a sale may not move a device another customer owns.
	ErrOtherTenant = errors.New("onboarding: the device belongs to another customer")
)

type userError struct {
	kind error
	msg  string
}

func (e userError) Error() string        { return e.msg }
func (e userError) Is(target error) bool { return target == e.kind }

func invalidf(format string, args ...any) error {
	return userError{ErrInvalid, fmt.Sprintf(format, args...)}
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

// --- sites -----------------------------------------------------------------

const siteColumns = `id, customer_id, external_ref, name, COALESCE(street,''), COALESCE(suburb,''), COALESCE(city,''),
	COALESCE(postal_code,''), COALESCE(country,''), latitude, longitude, region_id, created_at, updated_at`

func scanSite(row interface{ Scan(...any) error }) (*Site, error) {
	var s Site
	err := row.Scan(&s.ID, &s.CustomerID, &s.ExternalRef, &s.Name, &s.Street, &s.Suburb, &s.City, &s.PostalCode, &s.Country,
		&s.Latitude, &s.Longitude, &s.RegionID, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &s, err
}

func (r *Repository) CreateSite(ctx context.Context, s Site) (*Site, error) {
	if strings.TrimSpace(s.Name) == "" {
		return nil, invalidf("site name is required")
	}
	if (s.Latitude == nil) != (s.Longitude == nil) {
		return nil, invalidf("give both latitude and longitude, or neither")
	}
	site, err := scanSite(r.db.QueryRowContext(ctx, `
		INSERT INTO sites (id, customer_id, external_ref, name, street, suburb, city, postal_code, country, latitude, longitude, region_id)
		VALUES ($1, $2, NULLIF($3, ''), $4, NULLIF($5,''), NULLIF($6,''), NULLIF($7,''), NULLIF($8,''), NULLIF($9,''), $10, $11, $12)
		RETURNING `+siteColumns,
		uuid.NewString(), s.CustomerID, deref(s.ExternalRef), strings.TrimSpace(s.Name), s.Street, s.Suburb, s.City, s.PostalCode, s.Country,
		s.Latitude, s.Longitude, s.RegionID))
	if err != nil && strings.Contains(err.Error(), "sites_external_ref_idx") {
		return nil, userError{ErrExists, fmt.Sprintf("a site with reference %q already exists", deref(s.ExternalRef))}
	}
	return site, err
}

func (r *Repository) GetSite(ctx context.Context, id string) (*Site, error) {
	return scanSite(r.db.QueryRowContext(ctx, `SELECT `+siteColumns+` FROM sites WHERE id = $1`, id))
}

// ListSites returns all sites, or only the given tenants' when scoped: a
// site is an address, so one with no tenant is not shared with every
// tenant the way a plan is.
func (r *Repository) ListSites(ctx context.Context, customerIDs []string, scoped bool) ([]Site, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+siteColumns+` FROM sites
		WHERE NOT $1 OR customer_id::text = ANY($2) ORDER BY name`, scoped, store.StringArray(customerIDs))
	if err != nil {
		return nil, fmt.Errorf("list sites: %w", err)
	}
	defer rows.Close()
	out := []Site{}
	for rows.Next() {
		s, err := scanSite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// --- service plans -----------------------------------------------------------

const planColumns = `id, customer_id, code, name, template_ids, cellular_profile, allowed_plmns, require_discovery, created_at, updated_at`

func scanPlan(row interface{ Scan(...any) error }) (*ServicePlan, error) {
	var p ServicePlan
	var templates, plmns store.StringArray
	var cellular []byte
	err := row.Scan(&p.ID, &p.CustomerID, &p.Code, &p.Name, &templates, &cellular, &plmns, &p.RequireDiscovery, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.TemplateIDs, p.AllowedPLMNs = []string(templates), []string(plmns)
	if err := json.Unmarshal(cellular, &p.Cellular); err != nil {
		return nil, fmt.Errorf("decode cellular profile: %w", err)
	}
	return &p, nil
}

func (r *Repository) CreatePlan(ctx context.Context, p ServicePlan) (*ServicePlan, error) {
	if strings.TrimSpace(p.Code) == "" || strings.TrimSpace(p.Name) == "" {
		return nil, invalidf("plan code and name are required")
	}
	for _, plmn := range p.AllowedPLMNs {
		if !isPLMN(plmn) {
			return nil, invalidf("allowed network %q must be MCC+MNC: 5 or 6 digits, e.g. 65506", plmn)
		}
	}
	// Cellular paths are vendor fields found in the discovered model, so a
	// plan that sets them cannot skip discovery.
	if !p.Cellular.empty() {
		p.RequireDiscovery = true
	}
	cellular, err := json.Marshal(p.Cellular)
	if err != nil {
		return nil, err
	}
	plan, err := scanPlan(r.db.QueryRowContext(ctx, `
		INSERT INTO service_plans (id, customer_id, code, name, template_ids, cellular_profile, allowed_plmns, require_discovery)
		VALUES ($1, $2, $3, $4, COALESCE($5::uuid[], '{}'), $6, COALESCE($7::text[], '{}'), $8) RETURNING `+planColumns,
		uuid.NewString(), p.CustomerID, strings.TrimSpace(p.Code), strings.TrimSpace(p.Name), store.StringArray(p.TemplateIDs),
		cellular, store.StringArray(p.AllowedPLMNs), p.RequireDiscovery))
	if err != nil && strings.Contains(err.Error(), "service_plans_code_idx") {
		return nil, userError{ErrExists, fmt.Sprintf("a plan with code %q already exists", p.Code)}
	}
	return plan, err
}

// ErrAmbiguousPlan: more than one customer has a plan with this code and
// none is shared, so a caller that knows no customer cannot choose.
var ErrAmbiguousPlan = errors.New("more than one customer has a service plan with this code")

// PlanByCode finds a plan for a caller that knows only the BSS plan code:
// the shared plan with that code, else the one customer plan that has it.
func (r *Repository) PlanByCode(ctx context.Context, code string) (*ServicePlan, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+planColumns+` FROM service_plans WHERE code = $1
		ORDER BY customer_id NULLS FIRST LIMIT 2`, strings.TrimSpace(code))
	if err != nil {
		return nil, fmt.Errorf("find plan by code: %w", err)
	}
	defer rows.Close()
	var found []*ServicePlan
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		found = append(found, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	switch {
	case len(found) == 0:
		return nil, ErrNotFound
	case found[0].CustomerID == nil || len(found) == 1:
		return found[0], nil
	default:
		return nil, ErrAmbiguousPlan
	}
}

// PlanForCustomer finds a plan by code for one customer: its own plan with
// that code, else the shared one. Another customer's plan is never found.
func (r *Repository) PlanForCustomer(ctx context.Context, code, customerID string) (*ServicePlan, error) {
	return scanPlan(r.db.QueryRowContext(ctx, `SELECT `+planColumns+` FROM service_plans
		WHERE code = $1 AND (customer_id IS NULL OR customer_id = $2::uuid)
		ORDER BY customer_id NULLS LAST LIMIT 1`, strings.TrimSpace(code), customerID))
}

// SiteByRef finds a customer's site by its BSS premise reference.
func (r *Repository) SiteByRef(ctx context.Context, customerID *string, ref string) (*Site, error) {
	return scanSite(r.db.QueryRowContext(ctx, `SELECT `+siteColumns+` FROM sites
		WHERE external_ref = $2 AND COALESCE(customer_id::text, '') = COALESCE($1::text, '')`, customerID, strings.TrimSpace(ref)))
}

func (r *Repository) GetPlan(ctx context.Context, id string) (*ServicePlan, error) {
	return scanPlan(r.db.QueryRowContext(ctx, `SELECT `+planColumns+` FROM service_plans WHERE id = $1`, id))
}

// ListPlans returns all plans, or those of the given tenants plus global
// ones when scoped.
func (r *Repository) ListPlans(ctx context.Context, customerIDs []string, scoped bool) ([]ServicePlan, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+planColumns+` FROM service_plans
		WHERE NOT $1 OR customer_id IS NULL OR customer_id::text = ANY($2) ORDER BY code`, scoped, store.StringArray(customerIDs))
	if err != nil {
		return nil, fmt.Errorf("list plans: %w", err)
	}
	defer rows.Close()
	out := []ServicePlan{}
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func isPLMN(s string) bool {
	if len(s) != 5 && len(s) != 6 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// --- onboardings ---------------------------------------------------------------

const onboardingColumns = `id, customer_id, device_id, COALESCE(expected_oui,''), expected_serial, COALESCE(expected_imei,''),
	COALESCE(expected_iccid,''), COALESCE(expected_imsi,''), COALESCE(account_ref,''), site_id, service_plan_id, role,
	commercial_model, source, COALESCE(external_ref,''), state, COALESCE(state_reason,''), COALESCE(state_code,''), config_jobs,
	COALESCE(requested_by,''), COALESCE(bss_role,''), COALESCE(bss_service_plan,''), created_at, updated_at, contacted_at, in_service_at`

func scanOnboarding(row interface{ Scan(...any) error }) (*Onboarding, error) {
	var o Onboarding
	var jobs store.StringArray
	err := row.Scan(&o.ID, &o.CustomerID, &o.DeviceID, &o.ExpectedOUI, &o.ExpectedSerial, &o.ExpectedIMEI, &o.ExpectedICCID,
		&o.ExpectedIMSI, &o.AccountRef, &o.SiteID, &o.ServicePlanID, &o.Role, &o.CommercialModel, &o.Source, &o.ExternalRef,
		&o.State, &o.StateReason, &o.StateCode, &jobs, &o.RequestedBy, &o.BSSRole, &o.BSSServicePlan, &o.CreatedAt, &o.UpdatedAt, &o.ContactedAt, &o.InServiceAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	o.ConfigJobs = []string(jobs)
	return &o, err
}

// Create records a sale as a PLANNED onboarding. With DeviceID set it
// claims a device that has already connected.
func (r *Repository) Create(ctx context.Context, o Onboarding) (*Onboarding, error) {
	o.ExpectedSerial = strings.TrimSpace(o.ExpectedSerial)
	if o.ExpectedSerial == "" {
		return nil, invalidf("expected_serial is required")
	}
	if o.Role == "" {
		o.Role = "gateway"
	}
	if o.CommercialModel == "" {
		o.CommercialModel = "retail"
	}
	created, err := scanOnboarding(r.db.QueryRowContext(ctx, `
		INSERT INTO onboardings (id, customer_id, device_id, expected_oui, expected_serial, expected_imei, expected_iccid, expected_imsi,
			account_ref, site_id, service_plan_id, role, commercial_model, source, external_ref, state, requested_by, bss_role, bss_service_plan)
		VALUES ($1, $2, $3, NULLIF($4,''), $5, NULLIF($6,''), NULLIF($7,''), NULLIF($8,''), NULLIF($9,''), $10, $11, $12, $13, $14,
			NULLIF($15,''), 'PLANNED', NULLIF($16,''), NULLIF($17,''), NULLIF($18,''))
		RETURNING `+onboardingColumns,
		uuid.NewString(), o.CustomerID, o.DeviceID, strings.ToUpper(strings.TrimSpace(o.ExpectedOUI)), o.ExpectedSerial,
		strings.TrimSpace(o.ExpectedIMEI), strings.TrimSpace(o.ExpectedICCID), strings.TrimSpace(o.ExpectedIMSI), strings.TrimSpace(o.AccountRef),
		o.SiteID, o.ServicePlanID, o.Role, o.CommercialModel, o.Source, o.ExternalRef, o.RequestedBy, o.BSSRole, o.BSSServicePlan))
	if err != nil && strings.Contains(err.Error(), "onboardings_open_serial_idx") {
		return nil, ErrOpenOnboardingExists
	}
	return created, err
}

func (r *Repository) Get(ctx context.Context, id string) (*Onboarding, error) {
	return scanOnboarding(r.db.QueryRowContext(ctx, `SELECT `+onboardingColumns+` FROM onboardings WHERE id = $1`, id))
}

type ListFilter struct {
	State       string
	DeviceID    string
	CustomerIDs []string
	Scoped      bool
	Limit       int
}

func (r *Repository) List(ctx context.Context, f ListFilter) ([]Onboarding, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 500
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+onboardingColumns+` FROM onboardings
		WHERE ($1 = '' OR state = $1) AND (NOT $2 OR customer_id::text = ANY($3)) AND ($5 = '' OR device_id::text = $5)
		ORDER BY updated_at DESC LIMIT $4`, f.State, f.Scoped, store.StringArray(f.CustomerIDs), f.Limit, f.DeviceID)
	if err != nil {
		return nil, fmt.Errorf("list onboardings: %w", err)
	}
	defer rows.Close()
	out := []Onboarding{}
	for rows.Next() {
		o, err := scanOnboarding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

// OpenBySerial returns the open onboarding for a serial, if any (at most
// one exists: onboardings_open_serial_idx).
func (r *Repository) OpenBySerial(ctx context.Context, serial string) (*Onboarding, error) {
	return scanOnboarding(r.db.QueryRowContext(ctx, `SELECT `+onboardingColumns+` FROM onboardings
		WHERE expected_serial = $1 AND state NOT IN ('IN_SERVICE', 'CANCELLED')`, strings.TrimSpace(serial)))
}

// OpenForDevice finds the open onboarding for an informing device: the one
// already bound to it, else a planned one for its serial (and OUI, when the
// sale named one).
func (r *Repository) OpenForDevice(ctx context.Context, deviceID, oui, serial string) (*Onboarding, error) {
	return scanOnboarding(r.db.QueryRowContext(ctx, `SELECT `+onboardingColumns+` FROM onboardings
		WHERE state NOT IN ('IN_SERVICE', 'CANCELLED')
		  AND (device_id = $1 OR (device_id IS NULL AND expected_serial = $3 AND (expected_oui IS NULL OR expected_oui = upper($2))))
		ORDER BY device_id NULLS LAST LIMIT 1`, deviceID, oui, serial))
}

// Apply moves an onboarding to the decided state, only if it is still in
// the state it was read in (several gateways may evaluate at once). On
// first contact it binds the device and gives it the sale's tenant and
// site. configJobs, when non-nil, replaces the configuration job list.
func (r *Repository) Apply(ctx context.Context, o Onboarding, d Decision, deviceID string, configJobs []string) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		UPDATE onboardings SET state = $3, state_reason = NULLIF($4, ''), state_code = NULLIF($8, ''), device_id = COALESCE(device_id, NULLIF($5, '')::uuid),
			contacted_at = CASE WHEN contacted_at IS NULL AND $3 <> 'PLANNED' THEN now() ELSE contacted_at END,
			in_service_at = CASE WHEN $3 = 'IN_SERVICE' THEN now() ELSE in_service_at END,
			config_jobs = CASE WHEN $6 THEN COALESCE($7::text[], '{}') ELSE config_jobs END,
			updated_at = now()
		WHERE id = $1 AND state = $2`,
		o.ID, o.State, d.State, d.Reason, deviceID, configJobs != nil, store.StringArray(configJobs), d.Code)
	if err != nil {
		return false, fmt.Errorf("apply onboarding state: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	if o.State == StatePlanned && deviceID != "" {
		// Never move an owned device to another tenant: a sale recorded
		// for a serial another customer already has is refused here, in
		// the same transaction, whatever the caller checked before.
		res, err := tx.ExecContext(ctx, `UPDATE devices SET site_id = COALESCE($2, site_id), customer_id = COALESCE(customer_id, $3),
			last_updated_at = now() WHERE id = $1 AND (customer_id IS NULL OR $3::uuid IS NULL OR customer_id = $3::uuid)`,
			deviceID, o.SiteID, o.CustomerID)
		if err != nil {
			return false, fmt.Errorf("assign device to the sale: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			if err == nil {
				err = ErrOtherTenant
			}
			return false, err
		}
	}
	return true, tx.Commit()
}

// Retry reopens a FAILED or ON_HOLD onboarding from CONTACTED (PLANNED if
// it was never bound to a device, so binding runs again), so every
// check runs again on the next Inform.
func (r *Repository) Retry(ctx context.Context, id string) error {
	return r.exec(ctx, `UPDATE onboardings SET state = CASE WHEN device_id IS NULL THEN 'PLANNED' ELSE 'CONTACTED' END, state_reason = NULL, state_code = NULL, config_jobs = '{}', updated_at = now()
		WHERE id = $1 AND state IN ('FAILED', 'ON_HOLD')`, id)
}

func (r *Repository) Cancel(ctx context.Context, id string) error {
	return r.exec(ctx, `UPDATE onboardings SET state = 'CANCELLED', updated_at = now()
		WHERE id = $1 AND state NOT IN ('IN_SERVICE', 'CANCELLED')`, id)
}

// ExpireUncontacted cancels up to limit sales from source that no CPE has
// contacted within ttl (CodeTTLExpired), freeing their serials for a new
// sale, and returns them as cancelled. A sale whose CPE connected, even if
// it is held, never expires: an operator decides.
func (r *Repository) ExpireUncontacted(ctx context.Context, source string, ttl time.Duration, reason string, limit int) ([]Onboarding, error) {
	rows, err := r.db.QueryContext(ctx, `UPDATE onboardings SET state = 'CANCELLED', state_code = $3, state_reason = $4, updated_at = now()
		WHERE id IN (SELECT id FROM onboardings
			WHERE source = $1 AND state = 'PLANNED' AND device_id IS NULL AND updated_at < now() - make_interval(secs => $2::float8)
			ORDER BY updated_at LIMIT $5 FOR UPDATE SKIP LOCKED)
		RETURNING `+onboardingColumns, source, ttl.Seconds(), CodeTTLExpired, reason, limit)
	if err != nil {
		return nil, fmt.Errorf("expire sales: %w", err)
	}
	defer rows.Close()
	var out []Onboarding
	for rows.Next() {
		o, err := scanOnboarding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

func (r *Repository) exec(ctx context.Context, query, id string) error {
	res, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		if err != nil {
			return err
		}
		return ErrNotFound
	}
	return nil
}

// ConfigOutcome counts the onboarding's configuration jobs by outcome; a
// job not found yet counts as pending.
func (r *Repository) ConfigOutcome(ctx context.Context, commandKeys []string) (pending, done, failed int, firstError string, err error) {
	rows, err := r.db.QueryContext(ctx, `SELECT status, COALESCE(fault_string, '') FROM jobs WHERE command_key = ANY($1) ORDER BY created_at`,
		store.StringArray(commandKeys))
	if err != nil {
		return 0, 0, 0, "", fmt.Errorf("read configuration jobs: %w", err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var status, fault string
		if err := rows.Scan(&status, &fault); err != nil {
			return 0, 0, 0, "", err
		}
		seen++
		switch status {
		case "SUCCESS":
			done++
		case "FAILED", "TIMEOUT":
			failed++
			if firstError == "" {
				firstError = fault
				if firstError == "" {
					firstError = status
				}
			}
		default:
			pending++
		}
	}
	pending += len(commandKeys) - seen
	return pending, done, failed, firstError, rows.Err()
}

// UnclaimedDevice is a device that has never been onboarded.
type UnclaimedDevice struct {
	ID           string
	OUISerial    string
	Manufacturer string
	ProductClass string
	SerialNumber string
	OnlineStatus string
	LastInformAt *time.Time
}

// Unclaimed lists devices that reached the ACS without any onboarding, most
// recently seen first: nobody recorded who they belong to.
func (r *Repository) Unclaimed(ctx context.Context, limit int) ([]UnclaimedDevice, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT d.id, d.oui_serial, COALESCE(d.manufacturer,''), COALESCE(d.product_class,''), COALESCE(d.serial_number,''),
			COALESCE(d.online_status,''), d.last_inform_at
		FROM devices d
		WHERE NOT EXISTS (SELECT 1 FROM onboardings o WHERE o.device_id = d.id
				OR (o.device_id IS NULL AND o.state NOT IN ('IN_SERVICE','CANCELLED') AND o.expected_serial = d.serial_number))
		ORDER BY d.last_inform_at DESC NULLS LAST LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list unclaimed devices: %w", err)
	}
	defer rows.Close()
	out := []UnclaimedDevice{}
	for rows.Next() {
		var u UnclaimedDevice
		if err := rows.Scan(&u.ID, &u.OUISerial, &u.Manufacturer, &u.ProductClass, &u.SerialNumber, &u.OnlineStatus, &u.LastInformAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// DeviceOwners returns the customer of each known device with this serial
// (and OUI, when given); nil for a device with no customer. A sale for a
// serial that is already in the inventory must be allowed to see it.
func (r *Repository) DeviceOwners(ctx context.Context, oui, serial string) ([]*string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT customer_id::text FROM devices
		WHERE serial_number = $2 AND ($1 = '' OR upper(oui) = upper($1))`, strings.TrimSpace(oui), strings.TrimSpace(serial))
	if err != nil {
		return nil, fmt.Errorf("look up devices by serial: %w", err)
	}
	defer rows.Close()
	var out []*string
	for rows.Next() {
		var c sql.NullString
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		if c.Valid {
			out = append(out, &c.String)
		} else {
			out = append(out, nil)
		}
	}
	return out, rows.Err()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
