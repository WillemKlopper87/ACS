package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"acs/internal/simwatch/reads"
)

// SIM change watch. The ACS judges a replaced SIM at the parameter-cache write
// and stores only a verdict with masked hints (never a full IMSI or ICCID);
// this file serves that state on the SIM view and takes an operator's
// approval. Approving never writes to the CPE.

const minSIMApproveReason = 10

type simWatchView struct {
	State             string  `json:"state"`
	Network           string  `json:"network"`
	ICCIDHint         string  `json:"iccid_hint"`
	IMSIHint          string  `json:"imsi_hint"`
	PreviousICCIDHint string  `json:"previous_iccid_hint"`
	PreviousIMSIHint  string  `json:"previous_imsi_hint"`
	DetectedAt        *string `json:"detected_at,omitempty"`
	ApprovedAt        *string `json:"approved_at,omitempty"`
	ApprovedBy        string  `json:"approved_by,omitempty"`
}

func newSIMWatchView(s *reads.State) *simWatchView {
	if s == nil {
		return nil
	}
	v := &simWatchView{State: s.State, Network: s.Network, ICCIDHint: s.ICCIDHint, IMSIHint: s.IMSIHint,
		PreviousICCIDHint: s.PreviousICCIDHint, PreviousIMSIHint: s.PreviousIMSIHint, ApprovedBy: s.ApprovedBy}
	if s.DetectedAt != nil {
		t := s.DetectedAt.UTC().Format(time.RFC3339Nano)
		v.DetectedAt = &t
	}
	if s.ApprovedAt != nil {
		t := s.ApprovedAt.UTC().Format(time.RFC3339Nano)
		v.ApprovedAt = &t
	}
	return v
}

// approveSIMWatch accepts the SIM now in the device. The reason is mandatory
// and audited; the approval is refused when the audit cannot be written.
func (h *handler) approveSIMWatch(w http.ResponseWriter, r *http.Request) {
	device, ok := h.getScopedDevice(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
		// DetectedAt is the detection time of the verdict the operator was
		// shown; an approval only applies to that verdict.
		DetectedAt string `json:"detected_at"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if len(reason) < minSIMApproveReason {
		http.Error(w, "reason is required (at least 10 characters), e.g. why the new SIM is expected", http.StatusBadRequest)
		return
	}
	seen, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(req.DetectedAt))
	if err != nil {
		http.Error(w, "detected_at is required: the detection time of the SIM change being approved", http.StatusBadRequest)
		return
	}
	state, err := h.simWatch.Get(r.Context(), device.ID)
	if err != nil {
		h.logger.Error("failed to read SIM watch", "err", err, "device_id", device.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if state == nil || state.State == "ok" {
		http.Error(w, "no SIM change is awaiting approval", http.StatusConflict)
		return
	}
	if state.DetectedAt == nil || !state.DetectedAt.Equal(seen) {
		http.Error(w, "the SIM changed again since this was shown; review the new change first", http.StatusConflict)
		return
	}
	actor := operatorFromRequest(r)
	if err := h.auditor.Record(r.Context(), actor, device.ID, "SimChangeApproved", map[string]any{
		"reason": reason, "network": state.Network, "verdict": state.State,
	}); err != nil {
		h.logger.Error("failed to write audit record; SIM change not approved", "err", err, "device_id", device.ID)
		http.Error(w, "audit unavailable; approval refused", http.StatusServiceUnavailable)
		return
	}
	if err := h.simWatch.Approve(r.Context(), device.ID, actor, reason, seen, time.Now().UTC()); err != nil {
		if errors.Is(err, reads.ErrNothingToApprove) {
			http.Error(w, "no SIM change is awaiting approval", http.StatusConflict)
			return
		}
		h.logger.Error("failed to approve SIM change", "err", err, "device_id", device.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	state, err = h.simWatch.Get(r.Context(), device.ID)
	if err != nil || state == nil {
		h.logger.Error("failed to read SIM watch after approval", "err", err, "device_id", device.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, newSIMWatchView(state))
}
