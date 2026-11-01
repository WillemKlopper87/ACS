package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"acs/internal/onboarding"
	"acs/internal/parameters"
)

// Onboarding: a sale names where a CPE goes (site), what it gets (service
// plan) and whose it is (tenant, BSS account reference); cmd/acs moves it
// to IN_SERVICE as the CPE's Informs show it is the unit and SIM that were
// sold, secured, discovered and configured. Reads are open to every role;
// changes need tenancy.manage, the same permission as assigning a device
// to a customer.

type siteResponse struct {
	ID          string    `json:"id"`
	CustomerID  *string   `json:"customer_id"`
	ExternalRef *string   `json:"external_ref"`
	Name        string    `json:"name"`
	Street      string    `json:"street"`
	Suburb      string    `json:"suburb"`
	City        string    `json:"city"`
	PostalCode  string    `json:"postal_code"`
	Country     string    `json:"country"`
	Latitude    *float64  `json:"latitude"`
	Longitude   *float64  `json:"longitude"`
	RegionID    *string   `json:"region_id"`
	CreatedAt   time.Time `json:"created_at"`
}

func toSiteResponse(s *onboarding.Site) siteResponse {
	return siteResponse{ID: s.ID, CustomerID: s.CustomerID, ExternalRef: s.ExternalRef, Name: s.Name, Street: s.Street,
		Suburb: s.Suburb, City: s.City, PostalCode: s.PostalCode, Country: s.Country, Latitude: s.Latitude,
		Longitude: s.Longitude, RegionID: s.RegionID, CreatedAt: s.CreatedAt}
}

type planResponse struct {
	ID               string                     `json:"id"`
	CustomerID       *string                    `json:"customer_id"`
	Code             string                     `json:"code"`
	Name             string                     `json:"name"`
	TemplateIDs      []string                   `json:"template_ids"`
	Cellular         onboarding.CellularProfile `json:"cellular"`
	AllowedPLMNs     []string                   `json:"allowed_plmns"`
	RequireDiscovery bool                       `json:"require_discovery"`
	CreatedAt        time.Time                  `json:"created_at"`
}

func toPlanResponse(p *onboarding.ServicePlan) planResponse {
	return planResponse{ID: p.ID, CustomerID: p.CustomerID, Code: p.Code, Name: p.Name, TemplateIDs: nonNil(p.TemplateIDs),
		Cellular: p.Cellular, AllowedPLMNs: nonNil(p.AllowedPLMNs), RequireDiscovery: p.RequireDiscovery, CreatedAt: p.CreatedAt}
}

// onboardingResponse masks the expected SIM and handset identifiers the
// same way the parameter views do (POPIA/RICA): a sale's ICCID and IMSI
// are a subscriber's, and the full values only ever go to the CPE check.
type onboardingResponse struct {
	ID              string     `json:"id"`
	CustomerID      *string    `json:"customer_id"`
	DeviceID        *string    `json:"device_id"`
	ExpectedOUI     string     `json:"expected_oui"`
	ExpectedSerial  string     `json:"expected_serial"`
	ExpectedIMEI    string     `json:"expected_imei"`
	ExpectedICCID   string     `json:"expected_iccid"`
	ExpectedIMSI    string     `json:"expected_imsi"`
	AccountRef      string     `json:"account_ref"`
	SiteID          *string    `json:"site_id"`
	ServicePlanID   *string    `json:"service_plan_id"`
	Role            string     `json:"role"`
	CommercialModel string     `json:"commercial_model"`
	Source          string     `json:"source"`
	ExternalRef     string     `json:"external_ref"`
	State           string     `json:"state"`
	StateReason     string     `json:"state_reason"`
	ConfigJobs      []string   `json:"config_jobs"`
	RequestedBy     string     `json:"requested_by"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	ContactedAt     *time.Time `json:"contacted_at"`
	InServiceAt     *time.Time `json:"in_service_at"`
}

func toOnboardingResponse(o *onboarding.Onboarding) onboardingResponse {
	mask := func(kind, v string) string {
		if v == "" {
			return ""
		}
		return parameters.MaskSubscriberIdentifier(kind, v)
	}
	return onboardingResponse{ID: o.ID, CustomerID: o.CustomerID, DeviceID: o.DeviceID, ExpectedOUI: o.ExpectedOUI,
		ExpectedSerial: o.ExpectedSerial, ExpectedIMEI: mask(parameters.SubscriberIMEI, o.ExpectedIMEI),
		ExpectedICCID: mask(parameters.SubscriberICCID, o.ExpectedICCID), ExpectedIMSI: mask(parameters.SubscriberIMSI, o.ExpectedIMSI),
		AccountRef: o.AccountRef, SiteID: o.SiteID, ServicePlanID: o.ServicePlanID, Role: o.Role, CommercialModel: o.CommercialModel,
		Source: o.Source, ExternalRef: o.ExternalRef, State: o.State, StateReason: o.StateReason, ConfigJobs: nonNil(o.ConfigJobs),
		RequestedBy: o.RequestedBy, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt, ContactedAt: o.ContactedAt, InServiceAt: o.InServiceAt}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// writeOnboardingError maps the repository's errors to HTTP statuses.
func (h *handler) writeOnboardingError(w http.ResponseWriter, err error, what string) {
	switch {
	case errors.Is(err, onboarding.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, onboarding.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, onboarding.ErrExists), errors.Is(err, onboarding.ErrOpenOnboardingExists):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		h.logger.Error("onboarding request failed", "err", err, "what", what)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// requireCustomerInScope is the rule every tenant-owned create follows: a
// scoped operator must name a customer within their scope.
func (h *handler) requireCustomerInScope(w http.ResponseWriter, r *http.Request, customerID *string) bool {
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return false
	}
	if scoped && (customerID == nil || !deviceInScope(customerID, customerIDs)) {
		http.Error(w, "customer_id is required and must be within your assigned scope", http.StatusBadRequest)
		return false
	}
	return true
}

// visible reports whether the caller may see a tenant-owned record. A
// record with no tenant is visible to scoped operators only when shared
// is true (service plans are a shared catalog; sites and onboardings are
// not).
func (h *handler) visible(w http.ResponseWriter, r *http.Request, customerID *string, shared bool) bool {
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return false
	}
	if scoped && !(shared && customerID == nil) && !deviceInScope(customerID, customerIDs) {
		http.Error(w, "not found", http.StatusNotFound)
		return false
	}
	return true
}

// --- sites --------------------------------------------------------------------

type createSiteRequest struct {
	CustomerID  *string  `json:"customer_id"`
	ExternalRef *string  `json:"external_ref"`
	Name        string   `json:"name"`
	Street      string   `json:"street"`
	Suburb      string   `json:"suburb"`
	City        string   `json:"city"`
	PostalCode  string   `json:"postal_code"`
	Country     string   `json:"country"`
	Latitude    *float64 `json:"latitude"`
	Longitude   *float64 `json:"longitude"`
	RegionID    *string  `json:"region_id"`
}

func (h *handler) createSite(w http.ResponseWriter, r *http.Request) {
	var req createSiteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if !h.requireCustomerInScope(w, r, req.CustomerID) {
		return
	}
	site, err := h.onboarding.CreateSite(r.Context(), onboarding.Site{CustomerID: req.CustomerID, ExternalRef: req.ExternalRef,
		Name: req.Name, Street: req.Street, Suburb: req.Suburb, City: req.City, PostalCode: req.PostalCode, Country: req.Country,
		Latitude: req.Latitude, Longitude: req.Longitude, RegionID: req.RegionID})
	if err != nil {
		h.writeOnboardingError(w, err, "create site")
		return
	}
	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), "", "SiteCreated", map[string]any{
		"site_id": site.ID, "customer_id": site.CustomerID, "external_ref": site.ExternalRef,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	writeJSON(w, http.StatusCreated, toSiteResponse(site))
}

func (h *handler) listSites(w http.ResponseWriter, r *http.Request) {
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sites, err := h.onboarding.ListSites(r.Context(), customerIDs, scoped)
	if err != nil {
		h.writeOnboardingError(w, err, "list sites")
		return
	}
	items := make([]siteResponse, 0, len(sites))
	for i := range sites {
		items = append(items, toSiteResponse(&sites[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handler) getSite(w http.ResponseWriter, r *http.Request) {
	site, err := h.onboarding.GetSite(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeOnboardingError(w, err, "get site")
		return
	}
	if h.visible(w, r, site.CustomerID, false) {
		writeJSON(w, http.StatusOK, toSiteResponse(site))
	}
}

// --- service plans ------------------------------------------------------------

type createPlanRequest struct {
	CustomerID       *string                    `json:"customer_id"`
	Code             string                     `json:"code"`
	Name             string                     `json:"name"`
	TemplateIDs      []string                   `json:"template_ids"`
	Cellular         onboarding.CellularProfile `json:"cellular"`
	AllowedPLMNs     []string                   `json:"allowed_plmns"`
	RequireDiscovery bool                       `json:"require_discovery"`
}

func (h *handler) createServicePlan(w http.ResponseWriter, r *http.Request) {
	var req createPlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if !h.requireCustomerInScope(w, r, req.CustomerID) {
		return
	}
	for _, id := range req.TemplateIDs {
		t, err := h.templates.ByID(r.Context(), id)
		if err != nil {
			http.Error(w, "template "+id+" does not exist", http.StatusBadRequest)
			return
		}
		// A tenant's plan may use shared templates and its own, not another
		// tenant's.
		if t.CustomerID != nil && (req.CustomerID == nil || *t.CustomerID != *req.CustomerID) {
			http.Error(w, "template "+id+" belongs to another customer", http.StatusBadRequest)
			return
		}
	}
	plan, err := h.onboarding.CreatePlan(r.Context(), onboarding.ServicePlan{CustomerID: req.CustomerID, Code: req.Code, Name: req.Name,
		TemplateIDs: req.TemplateIDs, Cellular: req.Cellular, AllowedPLMNs: req.AllowedPLMNs, RequireDiscovery: req.RequireDiscovery})
	if err != nil {
		h.writeOnboardingError(w, err, "create service plan")
		return
	}
	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), "", "ServicePlanCreated", map[string]any{
		"plan_id": plan.ID, "code": plan.Code, "customer_id": plan.CustomerID, "template_ids": plan.TemplateIDs,
		"cellular": plan.Cellular, "allowed_plmns": plan.AllowedPLMNs,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	writeJSON(w, http.StatusCreated, toPlanResponse(plan))
}

func (h *handler) listServicePlans(w http.ResponseWriter, r *http.Request) {
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	plans, err := h.onboarding.ListPlans(r.Context(), customerIDs, scoped)
	if err != nil {
		h.writeOnboardingError(w, err, "list service plans")
		return
	}
	items := make([]planResponse, 0, len(plans))
	for i := range plans {
		items = append(items, toPlanResponse(&plans[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handler) getServicePlan(w http.ResponseWriter, r *http.Request) {
	plan, err := h.onboarding.GetPlan(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeOnboardingError(w, err, "get service plan")
		return
	}
	if h.visible(w, r, plan.CustomerID, true) {
		writeJSON(w, http.StatusOK, toPlanResponse(plan))
	}
}

// --- onboardings --------------------------------------------------------------

type createOnboardingRequest struct {
	CustomerID      *string `json:"customer_id"`
	DeviceID        *string `json:"device_id"`
	ExpectedOUI     string  `json:"expected_oui"`
	ExpectedSerial  string  `json:"expected_serial"`
	ExpectedIMEI    string  `json:"expected_imei"`
	ExpectedICCID   string  `json:"expected_iccid"`
	ExpectedIMSI    string  `json:"expected_imsi"`
	AccountRef      string  `json:"account_ref"`
	SiteID          *string `json:"site_id"`
	ServicePlanID   *string `json:"service_plan_id"`
	Role            string  `json:"role"`
	CommercialModel string  `json:"commercial_model"`
	ExternalRef     string  `json:"external_ref"`
}

var (
	onboardingRoles            = map[string]bool{"": true, "gateway": true, "extender": true, "ont": true, "other": true}
	onboardingCommercialModels = map[string]bool{"": true, "retail": true, "wholesale": true, "enterprise": true}
)

// createOnboarding records a sale (expected_serial, before the CPE is
// seen) or claims a CPE that already connected (device_id, from the
// unclaimed list).
func (h *handler) createOnboarding(w http.ResponseWriter, r *http.Request) {
	var req createOnboardingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if !onboardingRoles[req.Role] {
		http.Error(w, "role must be gateway, extender, ont or other", http.StatusBadRequest)
		return
	}
	if !onboardingCommercialModels[req.CommercialModel] {
		http.Error(w, "commercial_model must be retail, wholesale or enterprise", http.StatusBadRequest)
		return
	}
	if req.DeviceID != nil {
		device, ok := h.getScopedDevice(w, r, *req.DeviceID)
		if !ok {
			return
		}
		if req.ExpectedSerial != "" && !strings.EqualFold(strings.TrimSpace(req.ExpectedSerial), device.SerialNumber) {
			http.Error(w, "expected_serial does not match the device", http.StatusBadRequest)
			return
		}
		req.ExpectedSerial, req.ExpectedOUI = device.SerialNumber, device.OUI
		if req.CustomerID == nil {
			req.CustomerID = device.CustomerID
		}
	}
	if !h.requireCustomerInScope(w, r, req.CustomerID) {
		return
	}
	// A sale for a serial already in the inventory is a claim of that
	// device: the caller must be able to see it, and it must not belong to
	// another customer (cmd/acs would otherwise move it on first contact).
	if req.DeviceID == nil {
		owners, err := h.onboarding.DeviceOwners(r.Context(), req.ExpectedOUI, req.ExpectedSerial)
		if err != nil {
			h.writeOnboardingError(w, err, "look up devices by serial")
			return
		}
		customerIDs, scoped, err := h.deviceScope(r)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		for _, owner := range owners {
			if scoped && !deviceInScope(owner, customerIDs) {
				http.Error(w, "a device with this serial exists outside your scope", http.StatusConflict)
				return
			}
			if owner != nil && req.CustomerID != nil && *owner != *req.CustomerID {
				http.Error(w, "a device with this serial belongs to another customer", http.StatusConflict)
				return
			}
		}
	} else if device, ok := h.getScopedDevice(w, r, *req.DeviceID); !ok {
		return
	} else if device.CustomerID != nil && req.CustomerID != nil && *device.CustomerID != *req.CustomerID {
		http.Error(w, "the device belongs to another customer", http.StatusConflict)
		return
	}
	if req.SiteID != nil {
		site, err := h.onboarding.GetSite(r.Context(), *req.SiteID)
		if err != nil {
			http.Error(w, "site_id does not exist", http.StatusBadRequest)
			return
		}
		if site.CustomerID != nil && (req.CustomerID == nil || *site.CustomerID != *req.CustomerID) {
			http.Error(w, "the site belongs to another customer", http.StatusBadRequest)
			return
		}
	}
	if req.ServicePlanID != nil {
		plan, err := h.onboarding.GetPlan(r.Context(), *req.ServicePlanID)
		if err != nil {
			http.Error(w, "service_plan_id does not exist", http.StatusBadRequest)
			return
		}
		if plan.CustomerID != nil && (req.CustomerID == nil || *plan.CustomerID != *req.CustomerID) {
			http.Error(w, "the service plan belongs to another customer", http.StatusBadRequest)
			return
		}
	}
	actor := operatorFromRequest(r)
	o, err := h.onboarding.Create(r.Context(), onboarding.Onboarding{CustomerID: req.CustomerID, DeviceID: req.DeviceID,
		ExpectedOUI: req.ExpectedOUI, ExpectedSerial: req.ExpectedSerial, ExpectedIMEI: req.ExpectedIMEI, ExpectedICCID: req.ExpectedICCID,
		ExpectedIMSI: req.ExpectedIMSI, AccountRef: req.AccountRef, SiteID: req.SiteID, ServicePlanID: req.ServicePlanID, Role: req.Role,
		CommercialModel: req.CommercialModel, Source: "console", ExternalRef: req.ExternalRef, RequestedBy: actor})
	if err != nil {
		h.writeOnboardingError(w, err, "create onboarding")
		return
	}
	// No SIM or handset identifiers in the audit trail.
	if err := h.auditor.Record(r.Context(), actor, deref(o.DeviceID), "OnboardingCreated", map[string]any{
		"onboarding_id": o.ID, "expected_serial": o.ExpectedSerial, "customer_id": o.CustomerID, "site_id": o.SiteID,
		"service_plan_id": o.ServicePlanID, "account_ref": o.AccountRef, "claimed": o.DeviceID != nil,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	writeJSON(w, http.StatusCreated, toOnboardingResponse(o))
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (h *handler) listOnboardings(w http.ResponseWriter, r *http.Request) {
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	list, err := h.onboarding.List(r.Context(), onboarding.ListFilter{State: r.URL.Query().Get("state"), DeviceID: r.URL.Query().Get("device_id"), CustomerIDs: customerIDs, Scoped: scoped})
	if err != nil {
		h.writeOnboardingError(w, err, "list onboardings")
		return
	}
	items := make([]onboardingResponse, 0, len(list))
	for i := range list {
		items = append(items, toOnboardingResponse(&list[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handler) scopedOnboarding(w http.ResponseWriter, r *http.Request) (*onboarding.Onboarding, bool) {
	o, err := h.onboarding.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeOnboardingError(w, err, "get onboarding")
		return nil, false
	}
	if !h.visible(w, r, o.CustomerID, false) {
		return nil, false
	}
	return o, true
}

func (h *handler) getOnboarding(w http.ResponseWriter, r *http.Request) {
	if o, ok := h.scopedOnboarding(w, r); ok {
		writeJSON(w, http.StatusOK, toOnboardingResponse(o))
	}
}

// onboardingAction is cancel or retry: both act only from certain states
// and answer 409 from any other.
func (h *handler) onboardingAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		o, ok := h.scopedOnboarding(w, r)
		if !ok {
			return
		}
		var err error
		if action == "cancel" {
			err = h.onboarding.Cancel(r.Context(), o.ID)
		} else {
			err = h.onboarding.Retry(r.Context(), o.ID)
		}
		if errors.Is(err, onboarding.ErrNotFound) {
			http.Error(w, "cannot "+action+" an onboarding that is "+o.State, http.StatusConflict)
			return
		}
		if err != nil {
			h.writeOnboardingError(w, err, action+" onboarding")
			return
		}
		auditAction := map[string]string{"cancel": "OnboardingCancelled", "retry": "OnboardingRetried"}[action]
		if err := h.auditor.Record(r.Context(), operatorFromRequest(r), deref(o.DeviceID), auditAction, map[string]any{
			"onboarding_id": o.ID, "from": o.State,
		}); err != nil {
			h.logger.Error("failed to write audit record", "err", err)
		}
		updated, err := h.onboarding.Get(r.Context(), o.ID)
		if err != nil {
			h.writeOnboardingError(w, err, "get onboarding")
			return
		}
		writeJSON(w, http.StatusOK, toOnboardingResponse(updated))
	}
}

// listUnclaimedDevices lists CPEs that connected with nobody's sale on
// them. They have no tenant yet, so a scoped operator sees none.
func (h *handler) listUnclaimedDevices(w http.ResponseWriter, r *http.Request) {
	_, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if scoped {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}})
		return
	}
	devices, err := h.onboarding.Unclaimed(r.Context(), 500)
	if err != nil {
		h.writeOnboardingError(w, err, "list unclaimed devices")
		return
	}
	type item struct {
		ID           string     `json:"id"`
		OUISerial    string     `json:"oui_serial"`
		Manufacturer string     `json:"manufacturer"`
		ProductClass string     `json:"product_class"`
		SerialNumber string     `json:"serial_number"`
		OnlineStatus string     `json:"online_status"`
		LastInformAt *time.Time `json:"last_inform_at"`
	}
	items := make([]item, 0, len(devices))
	for _, d := range devices {
		items = append(items, item{d.ID, d.OUISerial, d.Manufacturer, d.ProductClass, d.SerialNumber, d.OnlineStatus, d.LastInformAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
