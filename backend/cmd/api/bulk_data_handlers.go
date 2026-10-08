package main

import (
	"acs/internal/bulkdata"
	"acs/internal/datamodel"
	"acs/internal/jobs"
	"acs/internal/parameters"
	"acs/internal/transfer"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type bulkDataConfiguration struct {
	ProfileInstance   int `json:"profile_instance"`
	ReportingInterval int `json:"reporting_interval"`
	Parameters        []struct {
		Instance int    `json:"instance"`
		Path     string `json:"path"`
	} `json:"parameters"`
}

func bulkProfileWrites(req bulkDataConfiguration, receiverURL, username, password string) (jobs.SetParameterPayload, error) {
	prefix := "Device.BulkData.Profile." + strconv.Itoa(req.ProfileInstance) + "."
	writes := []jobs.ParameterWrite{
		{Name: "Device.BulkData.Enable", Value: "true", Type: "xsd:boolean"},
		{Name: prefix + "Enable", Value: "true", Type: "xsd:boolean"},
		{Name: prefix + "Protocol", Value: "HTTP", Type: "xsd:string"},
		{Name: prefix + "EncodingType", Value: "JSON", Type: "xsd:string"},
		{Name: prefix + "ReportingInterval", Value: strconv.Itoa(req.ReportingInterval), Type: "xsd:unsignedInt"},
		{Name: prefix + "JSONEncoding.ReportFormat", Value: "NameValuePair", Type: "xsd:string"},
		{Name: prefix + "HTTP.URL", Value: receiverURL, Type: "xsd:string"},
		{Name: prefix + "HTTP.Username", Value: username, Type: "xsd:string"},
		{Name: prefix + "HTTP.Password", Value: password, Type: "xsd:string"},
	}
	seen := map[int]bool{}
	for _, p := range req.Parameters {
		if p.Instance < 1 || seen[p.Instance] || !bulkdata.IsMetricPath(p.Path) {
			return jobs.SetParameterPayload{}, fmt.Errorf("invalid or duplicate parameter instance/path")
		}
		seen[p.Instance] = true
		parameterPrefix := prefix + "Parameter." + strconv.Itoa(p.Instance) + "."
		writes = append(writes, jobs.ParameterWrite{Name: parameterPrefix + "Reference", Value: p.Path, Type: "xsd:string"}, jobs.ParameterWrite{Name: parameterPrefix + "Name", Value: p.Path, Type: "xsd:string"})
	}
	return jobs.SetParameterPayload{Parameters: writes, Verify: true}, nil
}

// configureBulkData uses already-instantiated, discovered profile / parameter
// rows. It never guesses instance numbers or silently creates vendor objects.
func (h *handler) configureBulkData(w http.ResponseWriter, r *http.Request) {
	device, ok := h.getScopedDevice(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if len(h.transferKey) == 0 {
		http.Error(w, "bulk collection requires transfer signing key", 503)
		return
	}
	var req bulkDataConfiguration
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.ProfileInstance < 1 || req.ReportingInterval < 60 || req.ReportingInterval > 86400 || len(req.Parameters) == 0 || len(req.Parameters) > 128 {
		http.Error(w, "existing profile instance, interval 60..86400, and 1..128 parameter rows are required", 400)
		return
	}
	base, err := url.Parse(h.uploadsBase)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil {
		http.Error(w, "bulk collection requires an HTTPS uploads base URL", 503)
		return
	}
	discovered, err := h.params.GetNames(r.Context(), device.ID)
	if err != nil {
		http.Error(w, "failed to read capabilities", 500)
		return
	}
	if discovered == nil {
		http.Error(w, "run parameter discovery before configuring bulk data", 409)
		return
	}
	// Validate capability evidence before allocating a receiver or queuing secrets.
	payload, err := bulkProfileWrites(req, "https://receiver.invalid", "receiver", "placeholder")
	if err != nil {
		http.Error(w, "invalid or duplicate parameter instance/path", 400)
		return
	}
	if err := h.validateWritableParameters(r.Context(), device.ID, payload); err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	paths := []string{}
	cache, err := h.params.Get(r.Context(), device.ID)
	if err != nil {
		http.Error(w, "failed to read firmware", 500)
		return
	}
	instances, err := h.params.Instances(r.Context(), device.ID)
	if err != nil {
		http.Error(w, "failed to read instances", 500)
		return
	}
	for _, p := range req.Parameters {
		_, exists := discovered.Names[p.Path]
		if !exists {
			_, exists = discovered.Names[datamodel.NormalizePath(p.Path)]
			if exists {
				if err := parameters.RequireInstances(p.Path, parameters.Firmware(cache), instances, time.Now()); err != nil {
					http.Error(w, err.Error(), 409)
					return
				}
			}
		}
		if !exists {
			http.Error(w, "metric path must exist in the discovered tree", 409)
			return
		}
		paths = append(paths, p.Path)
	}
	expires := time.Now().UTC().Add(7 * 24 * time.Hour)
	receiver, err := h.bulkData.Create(r.Context(), device.ID, req.ProfileInstance, paths, expires, req.ReportingInterval)
	if err != nil {
		http.Error(w, "failed to reserve bulk receiver", 500)
		return
	}
	token := transfer.Sign(h.transferKey, "bulk-data", receiver.ID, expires)
	receiverURL := strings.TrimRight(h.uploadsBase, "/") + "/api/v1/bulk-data/" + receiver.ID + "/receive"
	payload, _ = bulkProfileWrites(req, receiverURL, receiver.ID, token)
	job, err := h.jobs.Create(r.Context(), device.ID, jobs.TypeSetParameter, payload, operatorFromRequest(r))
	if err != nil {
		_ = h.bulkData.Revoke(r.Context(), receiver.ID, device.ID)
		http.Error(w, "failed to queue bulk profile", 500)
		return
	}
	_ = h.auditor.Record(r.Context(), operatorFromRequest(r), device.ID, "BulkDataConfigured", map[string]any{"receiver_id": receiver.ID, "command_key": job.CommandKey, "expires_at": expires})
	writeJSON(w, 202, map[string]any{"command_key": job.CommandKey, "status": job.Status, "receiver": receiver, "renew_before": expires})
}

func (h *handler) receiveBulkData(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		http.Error(w, "invalid receiver", 404)
		return
	}
	username, password, ok := r.BasicAuth()
	if !ok || username != id || len(h.transferKey) == 0 || transfer.Verify(h.transferKey, "bulk-data", id, password, time.Now()) != nil {
		http.Error(w, "invalid or expired receiver credentials", 403)
		return
	}
	receiver, err := h.bulkData.Get(r.Context(), id)
	if err != nil || receiver.Revoked || !receiver.ExpiresAt.After(time.Now()) {
		http.Error(w, "receiver unavailable", 403)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "application/json is required", 415)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var reader io.Reader = r.Body
	if encoding := strings.ToLower(r.Header.Get("Content-Encoding")); encoding == "gzip" {
		compressed, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, "invalid gzip body", 400)
			return
		}
		defer compressed.Close()
		reader = compressed
	} else if encoding != "" && encoding != "identity" {
		http.Error(w, "unsupported content encoding", 415)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		http.Error(w, "bulk report exceeds 1 MiB limit", 413)
		return
	}
	if err := bulkdata.ValidateReport(raw, receiver.Paths); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if _, err := h.bulkData.SaveReport(r.Context(), receiver, raw); err != nil {
		h.logger.Error("save bulk report", "receiver_id", id, "err", err)
		http.Error(w, "failed to persist report", 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) getBulkDataReports(w http.ResponseWriter, r *http.Request) {
	device, ok := h.getScopedDevice(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	reports, err := h.bulkData.Reports(r.Context(), device.ID)
	if err != nil {
		http.Error(w, "failed to read reports", 500)
		return
	}
	writeJSON(w, 200, map[string]any{"items": reports})
}

func (h *handler) revokeBulkData(w http.ResponseWriter, r *http.Request) {
	device, ok := h.getScopedDevice(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	id := r.PathValue("receiver_id")
	if _, err := uuid.Parse(id); err != nil {
		http.Error(w, "not found", 404)
		return
	}
	if err := h.bulkData.Revoke(r.Context(), id, device.ID); err != nil {
		http.Error(w, "receiver not found", 404)
		return
	}
	w.WriteHeader(204)
}
