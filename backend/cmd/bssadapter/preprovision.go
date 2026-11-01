// BSS pre-provisioning (docs/ONBOARDING.md): a TMF641 add, or a mapping
// request with pre_provision, for a CPE the ACS has never seen records a
// PLANNED onboarding instead of failing. cmd/acs moves the onboarding as
// the CPE checks in; runPreProvisionSyncLoop below follows it, writing the
// account mapping PENDING_ACTIVE on first contact and ACTIVE at IN_SERVICE,
// and completing the order item that asked for it.
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"acs/internal/bss"
	"acs/internal/onboarding"
)

const preProvisionSyncInterval = 15 * time.Second

// tmf641Place is where a pre-provisioned CPE is installed (TMF641's place
// reference, flattened): id is the BSS premise reference.
type tmf641Place struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Street     string `json:"street"`
	City       string `json:"city"`
	PostalCode string `json:"postalCode"`
	Country    string `json:"country"`
}

type preProvisionRequest struct {
	// CustomerID, when the caller names one, must be the account owner's.
	CustomerID  string
	AccountID   string
	ExternalRef string // the TMF641 externalId, when an order asked
	OUISerial   string
	Role        string
	ServicePlan string
	ICCID       string
	IMSI        string
	Place       *tmf641Place
}

// errPreProvisionUnavailable: the adapter runs without onboarding (tests
// and older deployments), so an unknown device fails as it always did.
var errPreProvisionUnavailable = errors.New("pre-provisioning is not available")

// errPreProvisionCustomer: the caller named a customer that does not own
// the account (for a legacy unowned account, any customer).
var errPreProvisionCustomer = errors.New("customer_id does not match the account's customer")

// onboardingRole maps the BSS role vocabulary onto the onboarding's; the
// BSS role itself is kept for the account mapping.
func onboardingRole(role string) string {
	switch role {
	case bss.RoleGateway, bss.RoleExtender, bss.RoleONT:
		return role
	default:
		return "other"
	}
}

// preProvision records a sale for a CPE that has not connected yet. It is
// idempotent per serial and account, so a retried order or a resumed item
// gets the same onboarding back.
func (h *handler) preProvision(ctx context.Context, req preProvisionRequest) (*onboarding.Onboarding, error) {
	if h.onboarding == nil {
		return nil, errPreProvisionUnavailable
	}
	role := roleOrDefault(req.Role)
	if !bss.ValidRole(role) {
		return nil, fmt.Errorf("%w: %s", bss.ErrInvalidRole, role)
	}
	oui, serial := bss.ParseOUISerial(req.OUISerial)
	if serial == "" {
		return nil, errors.New("ouiSerial has no serial number")
	}
	if existing, err := h.onboarding.OpenBySerial(ctx, serial); err == nil {
		if existing.AccountRef != req.AccountID {
			return nil, errors.New("this serial already has an open sale for another account")
		}
		return existing, nil
	} else if !errors.Is(err, onboarding.ErrNotFound) {
		return nil, err
	}

	// An account owned by a customer (a customer-bound client claimed it)
	// sells for that customer, and only its own or shared plans apply. A
	// legacy account with no owner falls back to the plan code: the shared
	// plan with that code, else the one customer plan that has it. An
	// unknown plan code is kept on the mapping but configures nothing.
	owner, err := h.mappings.AccountOwner(ctx, req.AccountID)
	if err != nil {
		return nil, err
	}
	if req.CustomerID != "" && req.CustomerID != owner {
		return nil, errPreProvisionCustomer
	}
	plan, err := h.resolvePlan(ctx, req.ServicePlan, owner)
	if err != nil {
		return nil, err
	}
	var customerID, planID *string
	if plan != nil {
		customerID, planID = plan.CustomerID, &plan.ID
	}
	if owner != "" {
		customerID = &owner
	}

	// A device with this serial in another customer's inventory is never
	// moved by a sale (the reconciler and store refuse it too).
	owners, err := h.onboarding.DeviceOwners(ctx, oui, serial)
	if err != nil {
		return nil, err
	}
	for _, owner := range owners {
		if owner != nil && customerID != nil && *owner != *customerID {
			return nil, errors.New("a device with this serial belongs to another customer")
		}
	}

	var siteID *string
	if req.Place != nil && strings.TrimSpace(req.Place.ID) != "" {
		site, err := h.onboarding.SiteByRef(ctx, customerID, req.Place.ID)
		if errors.Is(err, onboarding.ErrNotFound) {
			name := strings.TrimSpace(req.Place.Name)
			if name == "" {
				name = req.Place.ID
			}
			ref := strings.TrimSpace(req.Place.ID)
			site, err = h.onboarding.CreateSite(ctx, onboarding.Site{CustomerID: customerID, ExternalRef: &ref, Name: name,
				Street: req.Place.Street, City: req.Place.City, PostalCode: req.Place.PostalCode, Country: req.Place.Country})
		}
		if err != nil {
			return nil, fmt.Errorf("site %s: %w", req.Place.ID, err)
		}
		siteID = &site.ID
	}

	o, err := h.onboarding.Create(ctx, onboarding.Onboarding{
		CustomerID: customerID, ExpectedOUI: oui, ExpectedSerial: serial, ExpectedICCID: req.ICCID, ExpectedIMSI: req.IMSI,
		AccountRef: req.AccountID, SiteID: siteID, ServicePlanID: planID, Role: onboardingRole(role), Source: "bss",
		ExternalRef: req.ExternalRef, RequestedBy: "bss:" + req.AccountID, BSSRole: role, BSSServicePlan: strings.TrimSpace(req.ServicePlan),
	})
	if err != nil {
		return nil, err
	}
	// No SIM identifiers in the audit trail.
	if err := h.auditor.Record(ctx, "bss:"+req.AccountID, "", "OnboardingPreProvisioned", map[string]any{
		"onboarding_id": o.ID, "account_id": req.AccountID, "oui_serial": req.OUISerial, "service_plan": req.ServicePlan,
		"plan_matched": plan != nil, "external_id": req.ExternalRef,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	return o, nil
}

// resolvePlan finds the ACS service plan a BSS plan code means for an
// account's owner (its own or a shared plan), or for an account with no
// owner the shared plan with that code, else the one customer plan that
// has it. No code, or an unknown one, is a nil plan.
func (h *handler) resolvePlan(ctx context.Context, code, owner string) (*onboarding.ServicePlan, error) {
	code = strings.TrimSpace(code)
	if code == "" || h.onboarding == nil {
		return nil, nil
	}
	var p *onboarding.ServicePlan
	var err error
	if owner != "" {
		p, err = h.onboarding.PlanForCustomer(ctx, code, owner)
	} else {
		p, err = h.onboarding.PlanByCode(ctx, code)
	}
	if errors.Is(err, onboarding.ErrNotFound) {
		return nil, nil
	}
	return p, err
}

func (h *handler) runPreProvisionSyncLoop(ctx context.Context) {
	ticker := time.NewTicker(preProvisionSyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.syncPreProvisioned(ctx)
		}
	}
}

// syncPreProvisioned brings account mappings and waiting order items up to
// their onboardings. Mappings go first, so an item whose CPE reached
// IN_SERVICE completes with its mapping in the same pass.
func (h *handler) syncPreProvisioned(ctx context.Context) {
	h.sweepSecrets(ctx)
	if h.onboarding == nil {
		return
	}
	h.expireSales(ctx)
	pending, err := h.mappings.PreProvisionedNeedingSync(ctx, 100)
	if err != nil {
		h.logger.Error("failed to list pre-provisioned onboardings", "err", err)
		return
	}
	for _, p := range pending {
		if err := h.syncMapping(ctx, p); err != nil {
			h.logger.Error("failed to sync pre-provisioned mapping", "err", err, "onboarding_id", p.OnboardingID, "account_id", p.AccountID)
		}
	}

	items, err := h.mappings.AwaitingDeviceItems(ctx, 100)
	if err != nil {
		h.logger.Error("failed to list order items awaiting a device", "err", err)
		return
	}
	for _, a := range items {
		if err := h.syncAwaitingItem(ctx, a); err != nil {
			h.logger.Error("failed to sync order item awaiting a device", "err", err, "item_id", a.ItemID)
		}
	}
}

func (h *handler) syncMapping(ctx context.Context, p bss.PreProvisioned) error {
	switch {
	case p.MappingID == "":
		status := bss.StatusPendingActive
		if p.State == onboarding.StateInService {
			status = bss.StatusActive
		}
		m, err := h.mappings.AssignDeviceStatus(ctx, p.AccountID, p.DeviceOUISerial, p.Role, p.ServicePlan, status)
		if errors.Is(err, bss.ErrDeviceAlreadyAssigned) || errors.Is(err, bss.ErrRoleAlreadyAssigned) {
			// Already mapped by another path (a retried order): adopt it if
			// it is this device; otherwise the role is someone else's and an
			// operator must sort it out — the order item says so.
			active, lookupErr := h.mappings.ActiveDeviceForAccount(ctx, p.AccountID, p.Role)
			if lookupErr != nil || active.DeviceID != p.DeviceID {
				return err
			}
			m = active
		} else if err != nil {
			return err
		}
		if err := h.mappings.SetOnboardingMapping(ctx, p.OnboardingID, m.ID); err != nil {
			return err
		}
		if err := h.auditor.Record(ctx, "bss:"+p.AccountID, p.DeviceID, "BSSMappingCreated", map[string]any{
			"account_id": p.AccountID, "oui_serial": p.DeviceOUISerial, "service_plan": p.ServicePlan, "role": p.Role,
			"status": m.Status, "onboarding_id": p.OnboardingID,
		}); err != nil {
			h.logger.Error("failed to write audit record", "err", err)
		}
		return nil
	case p.State == onboarding.StateInService:
		return h.mappings.SetMappingStatus(ctx, p.MappingID, bss.StatusActive)
	case p.State == onboarding.StateCancelled:
		return h.mappings.EndMapping(ctx, p.MappingID, bss.ReasonCorrected)
	}
	return nil
}

func (h *handler) syncAwaitingItem(ctx context.Context, a bss.AwaitingItem) error {
	item := bss.ServiceOrderItem{ID: a.ItemID, OrderID: a.OrderID, Seq: a.Seq}
	switch {
	case a.OrderCancelled:
		if err := h.onboarding.Cancel(ctx, a.OnboardingID); err != nil && !errors.Is(err, onboarding.ErrNotFound) {
			return err
		}
		if err := h.mappings.UpdateServiceOrderItem(ctx, a.ItemID, "SKIPPED", "service order cancelled"); err != nil {
			return err
		}
		return h.skipTMF641PendingItems(ctx, a.OrderID, a.Seq)
	case a.State == onboarding.StateCancelled:
		message := "onboarding cancelled"
		if a.Reason != "" {
			message += ": " + a.Reason
		}
		return h.finishTMF641Item(ctx, item, "FAILED", message)
	case a.State == onboarding.StateInService && a.MappingID != "":
		if err := h.mappings.SetServiceOrderItemMapping(ctx, a.ItemID, a.MappingID); err != nil {
			return err
		}
		return h.finishTMF641Item(ctx, item, "COMPLETED", "")
	}
	// Still on its way (or held): say where, so a BSS reading the order
	// sees why it is not done. ON_HOLD and FAILED wait for an operator's
	// Retry in the console rather than failing the order.
	note := strings.ToLower(strings.ReplaceAll(a.State, "_", " "))
	switch {
	case a.State == onboarding.StatePlanned:
		note = "waiting for the device to connect"
	case a.State == onboarding.StateInService:
		note = "in service; waiting for the account mapping"
	case a.Reason != "":
		note += ": " + a.Reason
	}
	if note == a.LastError {
		return nil
	}
	return h.mappings.NoteAwaitingItem(ctx, a.ItemID, note)
}
