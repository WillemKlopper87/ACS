package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"acs/internal/jobs"
	"acs/internal/lifecycle"
	"acs/internal/onboarding"
	"acs/internal/operators"
)

// After a sale is in service (internal/lifecycle): end the service
// (offboard), swap the device, or move the sale to another site or plan.
// All need tenancy.manage and a sale in the caller's scope.

// endedReenrollWindow is how long a factory-reset, released device may
// come back with the bootstrap credential.
const endedReenrollWindow = 24 * time.Hour

func (h *handler) writeLifecycleError(w http.ResponseWriter, err error, what string) {
	switch {
	case errors.Is(err, lifecycle.ErrNotInService):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, lifecycle.ErrSerialInUse), errors.Is(err, lifecycle.ErrDeviceMoved):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, lifecycle.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		h.writeOnboardingError(w, err, what)
	}
}

// hasPermission is requirePermission as a check, for an option that needs
// more than the route's own permission.
func (h *handler) hasPermission(r *http.Request, perm string) (bool, error) {
	if len(h.jwtSecret) == 0 {
		return true, nil
	}
	claims, ok := operatorClaims(r.Context())
	if !ok {
		return false, nil
	}
	return h.permissions.Has(r.Context(), claims.Role, perm)
}

// publishSaleEvent records the TMF688 OnboardingStateChangeEvent the
// gateway publishes for its own transitions, so an integration hears of
// an ended or reconfigured sale too.
func (h *handler) publishSaleEvent(r *http.Request, o *onboarding.Onboarding, to, reason string) {
	if h.bssMappings == nil {
		return
	}
	at := time.Now().UTC()
	payload, _ := json.Marshal(map[string]any{
		"onboardingId": o.ID, "externalId": o.ExternalRef, "serialNumber": o.ExpectedSerial,
		"state": to, "previousState": o.State, "reason": reason, "source": o.Source,
	})
	key := fmt.Sprintf("onboarding-%s-%s-%s-%d", o.ID, o.State, to, at.UnixNano())
	var err error
	if o.CustomerID != nil {
		_, err = h.bssMappings.CreateCustomerEvent(r.Context(), *o.CustomerID, key, o.AccountRef, deref(o.DeviceID), "OnboardingStateChangeEvent", payload, at)
	} else {
		_, err = h.bssMappings.CreateEvent(r.Context(), key, o.AccountRef, deref(o.DeviceID), "", "OnboardingStateChangeEvent", payload, at)
	}
	if err != nil {
		h.logger.Error("failed to publish onboarding event", "err", err, "onboarding_id", o.ID)
	}
}

func decodeStrict(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// endService is POST /api/v1/onboardings/{id}/end:
// {"reason": "return|rma|upgrade|moved|corrected", "keep_subscriber": false, "factory_reset": false}.
// The device is released to the unassigned pool; factory_reset (needs
// devices.write and credential.manage) also queues a reset and opens a
// re-enrolment window so the reset unit can connect again.
func (h *handler) endService(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason         string `json:"reason"`
		KeepSubscriber bool   `json:"keep_subscriber"`
		FactoryReset   bool   `json:"factory_reset"`
	}
	if err := decodeStrict(r, &req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	o, ok := h.scopedOnboarding(w, r)
	if !ok {
		return
	}
	if req.FactoryReset {
		for _, perm := range []string{operators.PermDevicesWrite, operators.PermCredentialManage} {
			granted, err := h.hasPermission(r, perm)
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if !granted {
				http.Error(w, "a factory reset needs "+perm, http.StatusForbidden)
				return
			}
		}
	}
	actor := operatorFromRequest(r)
	res, err := h.lifecycle.End(r.Context(), lifecycle.EndRequest{OnboardingID: o.ID, Reason: req.Reason, Actor: actor, KeepProfiles: req.KeepSubscriber})
	if err != nil {
		h.writeLifecycleError(w, err, "end service")
		return
	}
	resetStatus := ""
	if req.FactoryReset {
		resetStatus = h.resetReleasedDevice(r, res.DeviceID, actor)
	}
	if err := h.auditor.Record(r.Context(), actor, res.DeviceID, "OnboardingEnded", map[string]any{
		"onboarding_id": o.ID, "reason": req.Reason, "accounts": res.Accounts, "subscribers_archived": len(res.Archived),
		"factory_reset": resetStatus,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.publishSaleEvent(r, o, onboarding.StateEnded, req.Reason)
	writeJSON(w, http.StatusOK, map[string]any{
		"onboarding_id": o.ID, "device_id": res.DeviceID, "accounts": nonNil(res.Accounts), "subscribers_archived": nonNil(res.Archived),
		"factory_reset": resetStatus,
	})
}

// resetReleasedDevice queues a factory reset and opens the re-enrolment
// window; it reports "queued" or what failed (the service is already
// ended, so a failure here is reported, not rolled back).
func (h *handler) resetReleasedDevice(r *http.Request, deviceID, actor string) string {
	job, err := h.jobs.Create(r.Context(), deviceID, jobs.TypeFactoryReset, jobs.FactoryResetPayload{}, actor)
	if err != nil {
		h.logger.Error("failed to queue factory reset", "err", err, "device_id", deviceID)
		return "failed: could not queue the reset"
	}
	until := time.Now().Add(endedReenrollWindow)
	if err := h.devices.OpenReenrollment(r.Context(), deviceID, until); err != nil {
		h.logger.Error("failed to open re-enrolment", "err", err, "device_id", deviceID)
		return "queued; re-enrolment window not opened"
	}
	if err := h.auditor.Record(r.Context(), actor, deviceID, "FactoryResetQueued", map[string]any{"command_key": job.CommandKey, "reason": "service ended"}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	if err := h.auditor.Record(r.Context(), actor, deviceID, "DeviceReenrollmentOpened", map[string]any{
		"until": until.Format(time.RFC3339), "reason": "service ended",
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	return "queued"
}

// swapDevice is POST /api/v1/onboardings/{id}/swap:
// {"reason": "rma|upgrade", "serial": "...", "oui": "..."}. The old unit's
// service ends and a sale for the replacement is recorded with the same
// account, site and plan; the subscriber profile stays.
func (h *handler) swapDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
		Serial string `json:"serial"`
		OUI    string `json:"oui"`
	}
	if err := decodeStrict(r, &req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	o, ok := h.scopedOnboarding(w, r)
	if !ok {
		return
	}
	if !h.serialSellable(w, r, req.OUI, req.Serial, o.CustomerID) {
		return
	}
	actor := operatorFromRequest(r)
	res, newID, err := h.lifecycle.Swap(r.Context(), lifecycle.SwapRequest{OnboardingID: o.ID, Reason: req.Reason, NewSerial: req.Serial, NewOUI: req.OUI, Actor: actor})
	if err != nil {
		h.writeLifecycleError(w, err, "swap device")
		return
	}
	if err := h.auditor.Record(r.Context(), actor, res.DeviceID, "OnboardingDeviceSwapped", map[string]any{
		"onboarding_id": o.ID, "replacement_onboarding_id": newID, "reason": req.Reason, "replacement_serial": strings.TrimSpace(req.Serial),
		"accounts": res.Accounts,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.publishSaleEvent(r, o, onboarding.StateEnded, req.Reason)
	replacement, err := h.onboarding.Get(r.Context(), newID)
	if err != nil {
		h.writeOnboardingError(w, err, "get replacement sale")
		return
	}
	// The replacement is a new sale an integration has not heard of: it
	// gets the PLANNED event a BSS-created sale gets, with no previous state.
	created := *replacement
	created.State = ""
	h.publishSaleEvent(r, &created, onboarding.StatePlanned, "replacement after "+req.Reason)
	writeJSON(w, http.StatusOK, map[string]any{"ended_onboarding_id": o.ID, "replacement": toOnboardingResponse(replacement)})
}

// moveSaleSite is PUT /api/v1/onboardings/{id}/site: {"site_id": "..."}.
func (h *handler) moveSaleSite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SiteID string `json:"site_id"`
	}
	if err := decodeStrict(r, &req); err != nil || strings.TrimSpace(req.SiteID) == "" {
		http.Error(w, "send site_id", http.StatusBadRequest)
		return
	}
	o, ok := h.scopedOnboarding(w, r)
	if !ok {
		return
	}
	if err := h.lifecycle.MoveSite(r.Context(), o.ID, req.SiteID); err != nil {
		h.writeLifecycleError(w, err, "move sale")
		return
	}
	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), deref(o.DeviceID), "OnboardingSiteChanged", map[string]any{
		"onboarding_id": o.ID, "from": deref(o.SiteID), "to": req.SiteID,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.writeSale(w, r, o.ID)
}

// changeSalePlan is PUT /api/v1/onboardings/{id}/plan: {"service_plan_id": "..."}.
// A plan with configuration is applied on the device's next check-in.
func (h *handler) changeSalePlan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServicePlanID string `json:"service_plan_id"`
	}
	if err := decodeStrict(r, &req); err != nil || strings.TrimSpace(req.ServicePlanID) == "" {
		http.Error(w, "send service_plan_id", http.StatusBadRequest)
		return
	}
	o, ok := h.scopedOnboarding(w, r)
	if !ok {
		return
	}
	plan, err := h.onboarding.GetPlan(r.Context(), req.ServicePlanID)
	if errors.Is(err, onboarding.ErrNotFound) {
		http.Error(w, "no such service plan", http.StatusBadRequest)
		return
	}
	if err != nil {
		h.writeOnboardingError(w, err, "get plan")
		return
	}
	configures := plan.HasConfiguration()
	if err := h.lifecycle.ChangePlan(r.Context(), o.ID, plan.ID, configures); err != nil {
		h.writeLifecycleError(w, err, "change plan")
		return
	}
	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), deref(o.DeviceID), "OnboardingPlanChanged", map[string]any{
		"onboarding_id": o.ID, "from": deref(o.ServicePlanID), "to": plan.ID, "reconfigures": configures,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	if configures {
		h.publishSaleEvent(r, o, onboarding.StateDiscovered, "service plan changed")
	}
	h.writeSale(w, r, o.ID)
}

func (h *handler) writeSale(w http.ResponseWriter, r *http.Request, id string) {
	updated, err := h.onboarding.Get(r.Context(), id)
	if err != nil {
		h.writeOnboardingError(w, err, "get onboarding")
		return
	}
	writeJSON(w, http.StatusOK, toOnboardingResponse(updated))
}
