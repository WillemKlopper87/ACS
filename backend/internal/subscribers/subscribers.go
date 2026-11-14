// Package subscribers finds the subscriber behind a CPE: the account it is
// mapped to, the account's profile (display name, VIP flag) and the site
// it is installed at. It backs the operator lookup (name, address, the
// last digits of a serial, account number, device label, VIP) and the
// readable device name the console shows instead of the raw oui_serial.
//
// Names and addresses are personal information: callers audit name and
// address lookups, and every query is limited to the caller's customers.
package subscribers

import (
	"acs/internal/accounts"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"acs/internal/store"
)

// Name and VIP sources.
const (
	SourceBSS      = "bss"
	SourceOperator = "operator"
)

// ErrNameFromBSS: the BSS supplied this account's name, so operators may
// not change it (the billing system is the source of truth).
var ErrNameFromBSS = errors.New("the subscriber name comes from the BSS and cannot be changed here")

// Profile is an account's row in account_profiles.
type Profile struct {
	AccountID   string    `json:"account_id"`
	DisplayName *string   `json:"display_name,omitempty"`
	NameSource  string    `json:"name_source,omitempty"`
	VIP         bool      `json:"vip"`
	VIPSource   string    `json:"vip_source,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
	UpdatedBy   string    `json:"updated_by,omitempty"`
}

// Site is the address a CPE is installed at.
type Site struct {
	Name       string `json:"name,omitempty"`
	Street     string `json:"street,omitempty"`
	Suburb     string `json:"suburb,omitempty"`
	City       string `json:"city,omitempty"`
	PostalCode string `json:"postal_code,omitempty"`
}

// Summary is what an operator needs to recognise a device's subscriber.
type Summary struct {
	DeviceID  string `json:"device_id"`
	AccountID string `json:"account_id,omitempty"`
	// AccountCustomerID is the customer that owns the account; absent when
	// nobody does. An account ID is unique per customer, so this is what
	// names the account in a later call (customer_id).
	AccountCustomerID *string `json:"account_customer_id,omitempty"`
	SubscriberName    *string `json:"subscriber_name,omitempty"`
	VIP               bool    `json:"vip"`
	Site              *Site   `json:"site,omitempty"`
	// DisplayName joins the parts that tell two subscribers apart, e.g.
	// "Jane Smith · ACC-10442 · Sandton · FastMile …0215".
	DisplayName string `json:"display_name"`
}

type Repository struct{ db *sql.DB }

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

// Get returns an account's profile, or nil.
func (r *Repository) Get(ctx context.Context, accountID string) (*Profile, error) {
	accountKey, err := accounts.Resolve(ctx, r.db, accountID)
	if err != nil {
		return nil, err
	}
	var p Profile
	var name, nameSrc, vipSrc, by sql.NullString
	err = r.db.QueryRowContext(ctx, `SELECT account_id, display_name, name_source, vip, vip_source, updated_at, updated_by
		FROM account_profiles WHERE account_key = $1`, accountKey).Scan(&p.AccountID, &name, &nameSrc, &p.VIP, &vipSrc, &p.UpdatedAt, &by)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get account profile: %w", err)
	}
	if name.Valid {
		p.DisplayName = &name.String
	}
	p.NameSource, p.VIPSource, p.UpdatedBy = nameSrc.String, vipSrc.String, by.String
	return &p, nil
}

// Set writes the given fields of an account's profile on behalf of source
// (SourceBSS or SourceOperator); nil fields stay as they are, and an empty
// name clears it. An operator cannot overwrite or clear a name the BSS
// supplied (ErrNameFromBSS); the BSS always can.
func (r *Repository) Set(ctx context.Context, accountID, source, actor string, name *string, vip *bool) (*Profile, error) {
	if name == nil && vip == nil {
		return r.Get(ctx, accountID)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	accountKey, err := accounts.Ensure(ctx, tx, accountID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO account_profiles (account_id, account_key) VALUES ($1, $2) ON CONFLICT DO NOTHING`, accountID, accountKey); err != nil {
		return nil, fmt.Errorf("create account profile: %w", err)
	}
	var curSource sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT name_source FROM account_profiles WHERE account_key = $1 FOR UPDATE`, accountKey).Scan(&curSource); err != nil {
		return nil, fmt.Errorf("lock account profile: %w", err)
	}
	if name != nil {
		if source == SourceOperator && curSource.String == SourceBSS {
			return nil, ErrNameFromBSS
		}
		var newName, newSource any
		if n := strings.TrimSpace(*name); n != "" {
			newName, newSource = n, source
		}
		if _, err := tx.ExecContext(ctx, `UPDATE account_profiles SET display_name = $2, name_source = $3, updated_at = now(), updated_by = $4 WHERE account_key = $1`,
			accountKey, newName, newSource, actor); err != nil {
			return nil, fmt.Errorf("set subscriber name: %w", err)
		}
	}
	if vip != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE account_profiles SET vip = $2, vip_source = $3, updated_at = now(), updated_by = $4 WHERE account_key = $1`,
			accountKey, *vip, source, actor); err != nil {
			return nil, fmt.Errorf("set VIP: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r.Get(ctx, accountID)
}

// AccountCustomers lists the customers that decide who may see an
// account's profile: its BSS owner alone when it has one; otherwise the
// customers of devices actively mapped to it, all of which must be in a
// caller's scope (a device of another customer mapped to an unowned
// account must not let that customer edit it).
func (r *Repository) AccountCustomers(ctx context.Context, accountID string) ([]string, error) {
	accountKey, err := accounts.Resolve(ctx, r.db, accountID)
	if err != nil {
		return nil, err
	}
	// The registry holds unowned accounts too: only a customer counts as
	// an owner.
	var owner string
	err = r.db.QueryRowContext(ctx, `SELECT COALESCE(customer_id::text, '') FROM bss_accounts WHERE id = $1`, accountKey).Scan(&owner)
	if err == nil && owner != "" {
		return []string{owner}, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("account owner: %w", err)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT d.customer_id::text FROM account_device_mappings m JOIN devices d ON d.id = m.device_id
		 WHERE m.account_key = $1 AND m.unassigned_at IS NULL AND d.customer_id IS NOT NULL`, accountKey)
	if err != nil {
		return nil, fmt.Errorf("account customers: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// summaryQuery selects one row per device: its active mapping (gateway
// role first), the account profile and the site. The profile is joined
// only when the account's BSS owner is the device's customer, or, for an
// unowned account, when every device mapped to it is that customer's (the
// rule accountInScope applies to edits), so a device of one customer mapped to another
// customer's account never shows that account's subscriber.
const summaryQuery = `
	SELECT d.id, d.oui_serial, COALESCE(d.serial_number, ''), COALESCE(d.manufacturer, ''), COALESCE(d.product_class, ''), d.label,
	       m.account_id, b.customer_id::text, p.display_name, COALESCE(p.vip, false),
	       s.name, s.street, s.suburb, s.city, s.postal_code
	  FROM devices d
	  LEFT JOIN LATERAL (
	        SELECT account_id, account_key FROM account_device_mappings
	         WHERE device_id = d.id AND unassigned_at IS NULL
	         ORDER BY (role = 'gateway') DESC, assigned_at DESC LIMIT 1) m ON true
	  LEFT JOIN bss_accounts b ON b.id = m.account_key
	  LEFT JOIN account_profiles p ON p.account_key = m.account_key
	        AND (b.customer_id = d.customer_id
	             OR (b.customer_id IS NULL AND NOT EXISTS (
	                    SELECT 1 FROM account_device_mappings m2 JOIN devices d2 ON d2.id = m2.device_id
	                     WHERE m2.account_key = m.account_key AND m2.unassigned_at IS NULL
	                       AND d2.customer_id IS DISTINCT FROM d.customer_id)))
	  LEFT JOIN sites s ON s.id = d.site_id`

type row struct {
	Summary
	ouiSerial, serial, manufacturer, model string
	label                                  *string
}

func scanRows(rows *sql.Rows) ([]row, error) {
	defer rows.Close()
	var out []row
	for rows.Next() {
		var x row
		var label, account, accountCustomer, name, sName, street, suburb, city, postal sql.NullString
		if err := rows.Scan(&x.DeviceID, &x.ouiSerial, &x.serial, &x.manufacturer, &x.model, &label,
			&account, &accountCustomer, &name, &x.VIP, &sName, &street, &suburb, &city, &postal); err != nil {
			return nil, err
		}
		if label.Valid && strings.TrimSpace(label.String) != "" {
			x.label = &label.String
		}
		x.AccountID = account.String
		if accountCustomer.Valid {
			x.AccountCustomerID = &accountCustomer.String
		}
		if name.Valid {
			x.SubscriberName = &name.String
		}
		if sName.Valid || street.Valid || suburb.Valid || city.Valid {
			x.Site = &Site{Name: sName.String, Street: street.String, Suburb: suburb.String, City: city.String, PostalCode: postal.String}
		}
		x.DisplayName = displayName(x)
		out = append(out, x)
	}
	return out, rows.Err()
}

// displayName joins who (subscriber name, else the operator's label),
// which account, where (suburb, else city) and which unit (model and the
// last four serial characters). Without any of them it is the oui_serial.
func displayName(x row) string {
	var parts []string
	switch {
	case x.SubscriberName != nil:
		parts = append(parts, *x.SubscriberName)
	case x.label != nil:
		parts = append(parts, *x.label)
	}
	if x.AccountID != "" {
		parts = append(parts, x.AccountID)
	}
	if x.Site != nil {
		if x.Site.Suburb != "" {
			parts = append(parts, x.Site.Suburb)
		} else if x.Site.City != "" {
			parts = append(parts, x.Site.City)
		}
	}
	if len(parts) == 0 {
		return x.ouiSerial
	}
	unit := strings.TrimSpace(x.model)
	if unit == "" {
		unit = x.manufacturer
	}
	if s := x.serial; len(s) > 4 {
		unit += " …" + s[len(s)-4:]
	} else if s != "" {
		unit += " " + s
	}
	return strings.Join(append(parts, strings.TrimSpace(unit)), " · ")
}

// Summaries returns the subscriber summary of each device (keyed by id).
func (r *Repository) Summaries(ctx context.Context, deviceIDs []string) (map[string]Summary, error) {
	out := map[string]Summary{}
	if len(deviceIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx, summaryQuery+` WHERE d.id::text = ANY($1)`, store.StringArray(deviceIDs))
	if err != nil {
		return nil, fmt.Errorf("device summaries: %w", err)
	}
	list, err := scanRows(rows)
	if err != nil {
		return nil, err
	}
	for _, x := range list {
		out[x.DeviceID] = x.Summary
	}
	return out, nil
}

// Match kinds, reported with each hit.
const (
	MatchSerial  = "serial"
	MatchAccount = "account"
	MatchName    = "name"
	MatchLabel   = "label"
	MatchAddress = "address"
)

// Hit is one lookup result.
type Hit struct {
	Summary
	OUISerial    string   `json:"oui_serial"`
	Manufacturer string   `json:"manufacturer,omitempty"`
	Model        string   `json:"model,omitempty"`
	Label        *string  `json:"label,omitempty"`
	Matched      []string `json:"matched"`
	score        int
}

// LookupParams narrows a lookup to the caller's customers.
type LookupParams struct {
	Query       string
	VIPOnly     bool
	CustomerIDs []string
	Scoped      bool
	Limit       int
}

// MaxLookupResults caps one lookup.
const MaxLookupResults = 50

// Lookup finds devices whose serial, account, subscriber name, label or
// address contains every word of the query (case-insensitive), or every
// VIP device when the query is empty and VIPOnly is set. Hits are ranked:
// an exact account or serial ending first, then account, serial, name,
// label and address matches.
func (r *Repository) Lookup(ctx context.Context, p LookupParams) ([]Hit, error) {
	words := strings.Fields(strings.ToLower(p.Query))
	if len(words) == 0 && !p.VIPOnly {
		return nil, nil
	}
	if len(words) > 6 {
		words = words[:6]
	}
	limit := p.Limit
	if limit <= 0 || limit > MaxLookupResults {
		limit = MaxLookupResults
	}

	var where []string
	var args []any
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	haystack := `lower(concat_ws(' ', d.oui_serial, d.label, m.account_id, p.display_name, s.name, s.street, s.suburb, s.city, s.postal_code))`
	for _, w := range words {
		where = append(where, haystack+" LIKE "+arg("%"+escapeLike(w)+"%")+` ESCAPE '\'`)
	}
	if p.VIPOnly {
		where = append(where, "p.vip")
	}
	if p.Scoped {
		where = append(where, "d.customer_id::text = ANY("+arg(store.StringArray(p.CustomerIDs))+")")
	}
	query := summaryQuery + " WHERE " + strings.Join(where, " AND ") + " ORDER BY d.last_inform_at DESC NULLS LAST LIMIT 200"
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("lookup: %w", err)
	}
	list, err := scanRows(rows)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(p.Query))
	hits := make([]Hit, 0, len(list))
	for _, x := range list {
		h := Hit{Summary: x.Summary, OUISerial: x.ouiSerial, Manufacturer: x.manufacturer, Model: x.model, Label: x.label, Matched: []string{}}
		has := func(field string) bool {
			f := strings.ToLower(field)
			for _, w := range words {
				if strings.Contains(f, w) {
					return true
				}
			}
			return false
		}
		account, serial := strings.ToLower(x.AccountID), strings.ToLower(x.ouiSerial)
		if q != "" && (account == q || strings.HasSuffix(serial, q)) {
			h.score += 100
		}
		if x.AccountID != "" && has(x.AccountID) {
			h.Matched, h.score = append(h.Matched, MatchAccount), h.score+40
		}
		if has(x.ouiSerial) {
			h.Matched, h.score = append(h.Matched, MatchSerial), h.score+30
		}
		if x.SubscriberName != nil && has(*x.SubscriberName) {
			h.Matched, h.score = append(h.Matched, MatchName), h.score+20
		}
		if x.label != nil && has(*x.label) {
			h.Matched, h.score = append(h.Matched, MatchLabel), h.score+15
		}
		if x.Site != nil && has(strings.Join([]string{x.Site.Name, x.Site.Street, x.Site.Suburb, x.Site.City, x.Site.PostalCode}, " ")) {
			h.Matched, h.score = append(h.Matched, MatchAddress), h.score+10
		}
		if x.VIP {
			h.score++
		}
		hits = append(hits, h)
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// PersonalMatch reports whether any hit matched on a subscriber name or an
// address: those lookups are audited.
func PersonalMatch(hits []Hit) bool {
	for _, h := range hits {
		for _, m := range h.Matched {
			if m == MatchName || m == MatchAddress {
				return true
			}
		}
	}
	return false
}
