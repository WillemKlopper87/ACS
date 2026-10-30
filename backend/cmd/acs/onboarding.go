// Onboarding reconciler: moves a sold CPE from PLANNED to IN_SERVICE as its
// Informs show what is true of it. The decisions live in
// internal/onboarding.Evaluate (pure, unit tested); this file only gathers
// the facts and carries out what was decided.
package main

import (
	"context"
	"errors"
	"fmt"

	"acs/internal/credentials"
	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/onboarding"
	"acs/internal/parameters"
)

// reconcileOnboarding evaluates the device's open onboarding, if any, on
// every Inform and again when the session runs out of jobs (onInform
// false), so a SIM read and the configuration it unlocks finish in one
// session. Only an Inform queues a SIM read: one that keeps failing must
// not loop the session. It reports whether it queued configuration. A
// device nobody planned is left alone: it shows up in the unclaimed list.
func (h *handler) reconcileOnboarding(ctx context.Context, device *devices.Device, onInform bool) (queued bool) {
	if h.onboarding == nil {
		return false
	}
	o, err := h.onboarding.OpenForDevice(ctx, device.ID, device.OUI, device.SerialNumber)
	if errors.Is(err, onboarding.ErrNotFound) {
		return false
	}
	if err != nil {
		h.logger.Error("failed to load onboarding for device", "err", err, "device_id", device.ID)
		return false
	}
	var plan *onboarding.ServicePlan
	if o.ServicePlanID != nil {
		if plan, err = h.onboarding.GetPlan(ctx, *o.ServicePlanID); err != nil {
			h.logger.Error("failed to load service plan", "err", err, "onboarding_id", o.ID)
			return false
		}
	}

	facts := onboarding.Facts{Informed: true, Secured: h.deviceSecured(ctx, device)}
	var names map[string]bool
	if dn, err := h.params.GetNames(ctx, device.ID); err == nil && dn != nil {
		names = dn.Names
	}
	facts.Discovered = len(names) > 0
	cache, err := h.params.Get(ctx, device.ID)
	if err != nil {
		h.logger.Error("failed to load parameter cache for onboarding", "err", err, "device_id", device.ID)
		return false
	}
	facts.ICCID, facts.IMSI = onboarding.SIMIdentifiers(cache)
	if len(o.ConfigJobs) > 0 {
		if facts.ConfigPending, facts.ConfigDone, facts.ConfigFailed, facts.ConfigError, err = h.onboarding.ConfigOutcome(ctx, o.ConfigJobs); err != nil {
			h.logger.Error("failed to read onboarding configuration outcome", "err", err, "onboarding_id", o.ID)
			return false
		}
	}

	d := onboarding.Evaluate(*o, plan, facts)
	bindTo := device.ID
	if o.State == onboarding.StatePlanned && device.CustomerID != nil && o.CustomerID != nil && *device.CustomerID != *o.CustomerID {
		// Apply refuses this too; holding says why instead of retrying on
		// every Inform. The device is not bound to a sale it may not join.
		d = onboarding.Decision{State: onboarding.StateOnHold, Reason: "the device already belongs to another customer"}
		bindTo = ""
	}
	creator := "onboarding:" + o.ID
	if d.ReadSIM && onInform {
		h.readSIMForOnboarding(ctx, device.ID, creator, names, &d)
	}
	var configJobs []string
	if d.Configure {
		if configJobs, err = h.queueOnboardingConfiguration(ctx, device.ID, creator, plan, names, cache); err != nil {
			d = onboarding.Decision{State: onboarding.StateFailed, Reason: "configuration could not be queued: " + err.Error()}
		}
	}
	if d.State == o.State && d.Reason == o.StateReason && configJobs == nil {
		return false
	}
	moved, err := h.onboarding.Apply(ctx, *o, d, bindTo, configJobs)
	if err != nil {
		h.logger.Error("failed to apply onboarding state", "err", err, "onboarding_id", o.ID)
		return len(configJobs) > 0
	}
	queued = len(configJobs) > 0
	if !moved || d.State == o.State {
		return queued
	}
	if err := h.auditor.Record(ctx, creator, device.ID, "OnboardingStateChanged", map[string]any{
		"onboarding_id": o.ID, "from": o.State, "to": d.State, "reason": d.Reason, "config_jobs": configJobs,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Info("onboarding advanced", "onboarding_id", o.ID, "device_id", device.ID, "from", o.State, "to", d.State)
	return queued
}

// reconcileOnboardingAtSessionEnd runs the reconciler for a session's
// device before the session closes, reporting whether it queued jobs.
func (h *handler) reconcileOnboardingAtSessionEnd(ctx context.Context, deviceID string) bool {
	if h.onboarding == nil {
		return false
	}
	device, err := h.devices.Get(ctx, deviceID)
	if err != nil {
		h.logger.Error("failed to load device for onboarding", "err", err, "device_id", deviceID)
		return false
	}
	return h.reconcileOnboarding(ctx, device, false)
}

// deviceSecured reports whether the CPE has left the shared bootstrap
// credential: it authenticates with its own Digest credential or a
// certificate.
func (h *handler) deviceSecured(ctx context.Context, device *devices.Device) bool {
	if device.CWMPAuthMode == devices.AuthModeMTLS {
		return true
	}
	if h.credentials == nil {
		return false
	}
	_, err := h.credentials.ActiveForDevice(ctx, device.ID, credentials.TypeCWMPDigest)
	if err != nil && !errors.Is(err, credentials.ErrNoActiveCredential) {
		h.logger.Error("failed to look up device credential", "err", err, "device_id", device.ID)
	}
	return err == nil
}

// readSIMForOnboarding queues one GET of the SIM identifiers. A device whose
// discovered model has none is not asked again on every Inform; it waits
// for an operator instead.
func (h *handler) readSIMForOnboarding(ctx context.Context, deviceID, creator string, names map[string]bool, d *onboarding.Decision) {
	paths := onboarding.SIMReadPaths(names)
	if len(names) > 0 {
		if _, hasSIM := names[paths[0]]; !hasSIM {
			d.Reason = "the device reports no SIM identifiers to check against the sale"
			return
		}
	}
	creator += ":sim"
	if open, err := h.jobs.HasOpenJobByCreator(ctx, deviceID, creator); err != nil || open {
		return
	}
	if _, err := h.jobs.Create(ctx, deviceID, jobs.TypeGetParameter, jobs.GetParameterPayload{Paths: paths}, creator); err != nil {
		h.logger.Error("failed to queue SIM read for onboarding", "err", err, "device_id", deviceID)
	}
}

// queueOnboardingConfiguration queues the plan's templates, then its
// cellular profile last: an APN change can drop the link, and everything
// else should be on the CPE before that risk is taken. It returns the
// command keys of what was queued. A template that cannot be loaded or
// queued fails the onboarding rather than letting it reach IN_SERVICE
// without part of the plan; what was queued before the error still runs.
func (h *handler) queueOnboardingConfiguration(ctx context.Context, deviceID, creator string, plan *onboarding.ServicePlan, names map[string]bool, cache map[string]parameters.CachedValue) ([]string, error) {
	keys := []string{}
	writes := [][]jobs.ParameterWrite{}
	for _, id := range plan.TemplateIDs {
		t, err := h.templates.ByID(ctx, id)
		if err != nil {
			return keys, fmt.Errorf("template %s: %w", id, err)
		}
		params := make([]jobs.ParameterWrite, len(t.Parameters))
		for i, p := range t.Parameters {
			params[i] = jobs.ParameterWrite{Name: p.Name, Value: p.Value, Type: p.Type}
		}
		writes = append(writes, params)
	}
	var cellular []jobs.ParameterWrite
	for _, w := range onboarding.CellularWrites(plan.Cellular, names, cache) {
		cellular = append(cellular, jobs.ParameterWrite{Name: w.Name, Value: w.Value, Type: w.Type})
	}
	writes = append(writes, cellular)
	for _, params := range writes {
		if len(params) == 0 {
			continue
		}
		job, err := h.jobs.Create(ctx, deviceID, jobs.TypeSetParameter, jobs.SetParameterPayload{Parameters: params, Verify: true}, creator)
		if err != nil {
			return keys, err
		}
		keys = append(keys, job.CommandKey)
	}
	return keys, nil
}
