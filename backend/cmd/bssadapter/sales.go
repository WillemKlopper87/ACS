package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"acs/internal/bss"
	"acs/internal/onboarding"
)

// defaultSaleTTL is how long a pre-provisioned sale waits for its CPE to
// connect before it is cancelled with TTL_EXPIRED.
const defaultSaleTTL = 30 * 24 * time.Hour

// saleTTLFromEnv reads ACS_BSS_SALE_TTL (a Go duration such as 720h; 0
// disables expiry). An unparsable value keeps the default.
func saleTTLFromEnv(logger *slog.Logger) time.Duration {
	raw := strings.TrimSpace(os.Getenv("ACS_BSS_SALE_TTL"))
	if raw == "" {
		return defaultSaleTTL
	}
	ttl, err := time.ParseDuration(raw)
	if err != nil || ttl < 0 {
		logger.Warn("ignoring invalid ACS_BSS_SALE_TTL", "value", raw, "default", defaultSaleTTL)
		return defaultSaleTTL
	}
	return ttl
}

// saleWarning flags something about a sale that will not stop it but that
// the BSS should fix, such as a plan code the ACS does not know.
type saleWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

const warningPlanNotFound = "PLAN_NOT_FOUND"

func planWarnings(code string, matched bool) []saleWarning {
	code = strings.TrimSpace(code)
	if code == "" || matched {
		return nil
	}
	return []saleWarning{{Code: warningPlanNotFound,
		Message: fmt.Sprintf("service plan %q matches no ACS service plan; the CPE is mapped but nothing is configured on it", code)}}
}

func saleHref(id string) string { return "/bss/v1/onboardings/" + id }

// saleResponse is a BSS sale as /bss/v1 shows it. No SIM identifiers: a
// reason carries only masked ones.
type saleResponse struct {
	ID           string        `json:"id"`
	Href         string        `json:"href"`
	AccountID    string        `json:"account_id"`
	ExternalID   string        `json:"external_id,omitempty"`
	OUI          string        `json:"oui,omitempty"`
	SerialNumber string        `json:"serial_number"`
	Role         string        `json:"role,omitempty"`
	ServicePlan  string        `json:"service_plan,omitempty"`
	PlanMatched  bool          `json:"plan_matched"`
	State        string        `json:"state"`
	ReasonCode   string        `json:"reason_code,omitempty"`
	Reason       string        `json:"reason,omitempty"`
	DeviceID     *string       `json:"device_id,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
	ContactedAt  *time.Time    `json:"contacted_at,omitempty"`
	InServiceAt  *time.Time    `json:"in_service_at,omitempty"`
	ExpiresAt    *time.Time    `json:"expires_at,omitempty"`
	Warnings     []saleWarning `json:"warnings,omitempty"`
}

func (h *handler) saleResponse(o *onboarding.Onboarding) saleResponse {
	utc := func(t *time.Time) *time.Time {
		if t == nil {
			return nil
		}
		u := t.UTC()
		return &u
	}
	s := saleResponse{
		ID: o.ID, Href: saleHref(o.ID), AccountID: o.AccountRef, ExternalID: o.ExternalRef, OUI: o.ExpectedOUI,
		SerialNumber: o.ExpectedSerial, Role: o.BSSRole, ServicePlan: o.BSSServicePlan, PlanMatched: o.ServicePlanID != nil,
		State: o.State, ReasonCode: o.StateCode, Reason: o.StateReason, DeviceID: o.DeviceID,
		CreatedAt: o.CreatedAt.UTC(), UpdatedAt: o.UpdatedAt.UTC(), ContactedAt: utc(o.ContactedAt), InServiceAt: utc(o.InServiceAt),
		Warnings: planWarnings(o.BSSServicePlan, o.ServicePlanID != nil),
	}
	if o.State == onboarding.StatePlanned && o.DeviceID == nil && o.Source == "bss" && h.saleTTL > 0 {
		s.ExpiresAt = utc(ptr(o.CreatedAt.Add(h.saleTTL)))
	}
	return s
}

func ptr[T any](v T) *T { return &v }

// getSale is GET /bss/v1/onboardings/{id}: where a sale is, for the
// account's owner. Another customer's sale reads as not found.
func (h *handler) getSale(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var o *onboarding.Onboarding
	var err error
	if h.onboarding != nil {
		o, err = h.onboarding.Get(r.Context(), id)
	}
	if err != nil && !errors.Is(err, onboarding.ErrNotFound) {
		h.logger.Error("failed to load sale", "err", err)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	if o == nil || o.AccountRef == "" {
		// Authenticate before answering, so an anonymous caller cannot
		// tell an unknown id from a forbidden one.
		if h.authorizeBSSAccount(w, r, bss.ScopeTMFRead, "") {
			writeError(w, http.StatusNotFound, "ErrNotFound", "resource not found")
		}
		return
	}
	if !h.authorizeBSSAccount(w, r, bss.ScopeTMFRead, o.AccountRef) {
		return
	}
	writeJSON(w, http.StatusOK, h.saleResponse(o))
}

// expireSales cancels pre-provisioned sales no CPE contacted within the
// sale TTL and tells the BSS (OnboardingStateChangeEvent, TTL_EXPIRED).
// syncAwaitingItem then fails the TMF641 item that waited for the CPE.
func (h *handler) expireSales(ctx context.Context) {
	if h.onboarding == nil || h.saleTTL <= 0 {
		return
	}
	reason := fmt.Sprintf("no device contact within %s of the sale", h.saleTTL)
	expired, err := h.onboarding.ExpireUncontacted(ctx, "bss", h.saleTTL, reason, 100)
	if err != nil {
		h.logger.Error("failed to expire sales", "err", err)
		return
	}
	at := time.Now().UTC()
	for _, o := range expired {
		h.logger.Info("sale expired", "onboarding_id", o.ID, "account_id", o.AccountRef)
		if err := h.auditor.Record(ctx, "bss:"+o.AccountRef, "", "OnboardingExpired", map[string]any{
			"onboarding_id": o.ID, "account_id": o.AccountRef, "external_id": o.ExternalRef, "ttl": h.saleTTL.String(),
		}); err != nil {
			h.logger.Error("failed to write audit record", "err", err)
		}
		payload, _ := json.Marshal(map[string]any{
			"onboardingId": o.ID, "externalId": o.ExternalRef, "serialNumber": o.ExpectedSerial,
			"state": onboarding.StateCancelled, "previousState": onboarding.StatePlanned,
			"reason": o.StateReason, "reasonCode": onboarding.CodeTTLExpired, "source": o.Source,
		})
		key := fmt.Sprintf("onboarding-%s-%s-%s-%d", o.ID, onboarding.StatePlanned, onboarding.StateCancelled, at.UnixNano())
		customer := ""
		if o.CustomerID != nil {
			customer = *o.CustomerID
		}
		if _, err := h.mappings.CreateCustomerEvent(ctx, customer, key, o.AccountRef, "", "OnboardingStateChangeEvent", payload, at); err != nil {
			h.logger.Error("failed to publish onboarding event", "err", err, "onboarding_id", o.ID)
		}
	}
}
