package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"acs/internal/firmware"
	"acs/internal/jobs"
	"acs/internal/transfer"
)

// Expiring transfer-token lifetimes (audit P0.3). Firmware download
// URLs live long enough to survive a delayed Download RPC plus an
// offline window; upload receipt URLs only need to cover one Upload
// RPC round trip.
const (
	firmwareTokenTTL = 24 * time.Hour
	uploadTokenTTL   = 4 * time.Hour
)

// signedFirmwareURL builds the public download URL for a firmware
// image, carrying an expiring purpose-bound token when transfer
// signing is enabled (it always is outside dev mode).
func (h *handler) signedFirmwareURL(imageID string) string {
	url := fmt.Sprintf("%s/api/v1/firmware/images/%s/file", h.firmwareBase, imageID)
	if len(h.transferKey) > 0 {
		url += "?token=" + transfer.Sign(h.transferKey, "firmware", imageID, time.Now().Add(firmwareTokenTTL))
	}
	return url
}

// firmwareImageResponse is the v3 §7.6 firmware_images shape.
type firmwareImageResponse struct {
	ID            string `json:"id"`
	Vendor        string `json:"vendor"`
	Model         string `json:"model"`
	Version       string `json:"version"`
	Channel       string `json:"channel"`
	Filename      string `json:"filename"`
	FileSizeBytes int64  `json:"file_size_bytes"`
	SHA256        string `json:"sha256"`
	URL           string `json:"url"`
	CreatedAt     string `json:"created_at"`
}

func (h *handler) toFirmwareImageResponse(img *firmware.Image) firmwareImageResponse {
	return firmwareImageResponse{
		ID: img.ID, Vendor: img.Vendor, Model: img.Model, Version: img.Version, Channel: img.Channel,
		Filename: img.Filename, FileSizeBytes: img.FileSizeBytes, SHA256: img.SHA256,
		URL:       h.signedFirmwareURL(img.ID),
		CreatedAt: img.CreatedAt.Format(time.RFC3339),
	}
}

func (h *handler) listFirmwareImages(w http.ResponseWriter, r *http.Request) {
	list, err := h.firmware.List(r.Context())
	if err != nil {
		h.logger.Error("failed to list firmware images", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	items := make([]firmwareImageResponse, 0, len(list))
	for i := range list {
		items = append(items, h.toFirmwareImageResponse(&list[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

// uploadFirmwareImage accepts a multipart upload — vendor/model/version/
// channel fields plus the binary itself — validates it (checksum
// computed while streaming to disk, per v3 §9.4's "validate SHA256"
// staging step), and records the metadata. The binary never touches
// Postgres (v3 §9.4/§19.4).
func (h *handler) uploadFirmwareImage(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil { // 64 MiB header buffer; the file itself streams
		http.Error(w, "invalid multipart form", http.StatusBadRequest)
		return
	}

	vendor := r.FormValue("vendor")
	model := r.FormValue("model")
	version := r.FormValue("version")
	channel := r.FormValue("channel")
	if channel == "" {
		channel = "stable"
	}
	if vendor == "" || model == "" || version == "" {
		http.Error(w, "vendor, model, and version are required", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file is required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	id := uuid.New().String()
	sha256hex, size, err := h.firmwareFS.Save(id, file)
	if err != nil {
		h.logger.Error("failed to save firmware file", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	img, err := h.firmware.Create(r.Context(), firmware.Image{
		ID: id, Vendor: vendor, Model: model, Version: version, Channel: channel,
		Filename: header.Filename, FileSizeBytes: size, SHA256: sha256hex,
		ContentType: "application/octet-stream",
	})
	if err != nil {
		h.logger.Error("failed to record firmware image metadata", "err", err, "id", id)
		http.Error(w, "internal error (vendor/model/version/channel must be unique)", http.StatusInternalServerError)
		return
	}

	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), "", "FirmwarePublish", map[string]any{
		"firmware_image_id": img.ID, "vendor": vendor, "model": model, "version": version, "sha256": sha256hex, "size": size,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Info("firmware image published", "id", img.ID, "vendor", vendor, "model", model, "version", version, "size", size, "sha256", sha256hex)

	writeJSON(w, http.StatusCreated, h.toFirmwareImageResponse(img))
}

// serveFirmwareFile is the URL a CPE's Download RPC actually fetches —
// the "HTTPS static file host" v3 §9.4 calls for, standing in as local
// disk + this same process rather than a real S3/CDN (see
// internal/firmware's doc comment). http.ServeContent gives Range-request
// support for free, which v3 §9.4 recommends.
func (h *handler) serveFirmwareFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// This route is public (a CPE's Download RPC has no JWT), so the
	// expiring token on the URL is the entire authorization (audit
	// P0.3) — without it a firmware image ID is a permanent public URL.
	if len(h.transferKey) > 0 {
		if err := transfer.Verify(h.transferKey, "firmware", id, r.URL.Query().Get("token"), time.Now()); err != nil {
			http.Error(w, "missing, invalid, or expired download token", http.StatusForbidden)
			return
		}
	}
	img, err := h.firmware.Get(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.logger.Error("failed to get firmware image", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	f, err := h.firmwareFS.Open(id)
	if err != nil {
		h.logger.Error("failed to open firmware file", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", img.ContentType)
	http.ServeContent(w, r, img.Filename, img.CreatedAt, f)
}

type createFirmwareDownloadRequest struct {
	FirmwareImageID string `json:"firmware_image_id"`
	DelaySeconds    int    `json:"delay_seconds"`
}

// createFirmwareDownload queues a FIRMWARE_DOWNLOAD job (design doc v3
// §8.5/§9.1). Like every other write endpoint, this only ever queues —
// the actual Download RPC happens on the device's next session
// (cmd/acs's dispatch, wired in this phase).
func (h *handler) createFirmwareDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}

	var req createFirmwareDownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.FirmwareImageID == "" {
		http.Error(w, "firmware_image_id is required", http.StatusBadRequest)
		return
	}

	job, err := h.queueFirmwareDownload(r.Context(), id, req.FirmwareImageID, req.DelaySeconds, operatorFromRequest(r))
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "firmware image not found", http.StatusBadRequest)
		return
	}
	if err != nil {
		h.logger.Error("failed to queue firmware download", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"command_key": job.CommandKey,
		"status":      job.Status,
	})
}

// queueFirmwareDownload builds a FIRMWARE_DOWNLOAD job for one device —
// shared by the single-device REST endpoint above and the rollout
// dispatcher (rollout_handlers.go, build plan §4 Phase 7), so both build
// the exact same payload shape rather than two copies drifting apart.
func (h *handler) queueFirmwareDownload(ctx context.Context, deviceID, firmwareImageID string, delaySeconds int, operator string) (*jobs.Job, error) {
	img, err := h.firmware.Get(ctx, firmwareImageID)
	if err != nil {
		return nil, err
	}

	payload := jobs.FirmwareDownloadPayload{
		FirmwareImageID: img.ID,
		FileType:        "1 Firmware Upgrade Image",
		URL:             h.signedFirmwareURL(img.ID),
		FileSize:        img.FileSizeBytes,
		TargetFilename:  img.Filename,
		DelaySeconds:    delaySeconds,
	}
	return h.jobs.Create(ctx, deviceID, jobs.TypeFirmwareDownload, payload, operator)
}
