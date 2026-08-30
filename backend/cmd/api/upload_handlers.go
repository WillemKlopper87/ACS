// Upload RPC (nice-to-have feature backlog, TR-069 §A.3.2.7): the
// CPE-to-ACS direction of file transfer, mirroring firmware_handlers.go's
// Download shape in reverse. createDeviceUpload queues the RPC and
// reserves a receipt slot; receiveUpload is where the CPE's own PUT
// actually lands, independent of any CWMP session.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"acs/internal/jobs"
	"acs/internal/transfer"
	"acs/internal/uploads"
)

type createUploadRequest struct {
	FileType string `json:"file_type"`
}

type uploadedFileResponse struct {
	ID            string  `json:"id"`
	DeviceID      string  `json:"device_id"`
	FileType      string  `json:"file_type"`
	Status        string  `json:"status"`
	Filename      *string `json:"filename,omitempty"`
	FileSizeBytes *int64  `json:"file_size_bytes,omitempty"`
	SHA256        *string `json:"sha256,omitempty"`
	CreatedAt     string  `json:"created_at"`
	ReceivedAt    *string `json:"received_at,omitempty"`
}

func toUploadedFileResponse(f *uploads.UploadedFile) uploadedFileResponse {
	resp := uploadedFileResponse{
		ID: f.ID, DeviceID: f.DeviceID, FileType: f.FileType, Status: f.Status,
		Filename: f.Filename, FileSizeBytes: f.FileSizeBytes, SHA256: f.SHA256,
		CreatedAt: f.CreatedAt.Format(time.RFC3339),
	}
	if f.ReceivedAt != nil {
		s := f.ReceivedAt.Format(time.RFC3339)
		resp.ReceivedAt = &s
	}
	return resp
}

// createDeviceUpload reserves an upload slot (PENDING uploaded_files row)
// and queues an UPLOAD job pointed at this process's own receipt
// endpoint — the CPE PUTs the file back here independently of the CWMP
// session that dispatched the RPC.
func (h *handler) createDeviceUpload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}

	var req createUploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.FileType == "" {
		http.Error(w, "file_type is required (e.g. \"1 Vendor Configuration File\")", http.StatusBadRequest)
		return
	}

	operator := operatorFromRequest(r)
	slot, err := h.uploads.Create(r.Context(), id, req.FileType, operator)
	if err != nil {
		h.logger.Error("failed to reserve upload slot", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	url := fmt.Sprintf("%s/api/v1/uploads/%s/receive", h.uploadsBase, slot.ID)
	if len(h.transferKey) > 0 {
		url += "?token=" + transfer.Sign(h.transferKey, "upload", slot.ID, time.Now().Add(uploadTokenTTL))
	}
	job, err := h.jobs.Create(r.Context(), id, jobs.TypeUpload, jobs.UploadPayload{FileType: req.FileType, URL: url}, operator)
	if err != nil {
		h.logger.Error("failed to queue upload", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"command_key": job.CommandKey, "status": job.Status, "upload_id": slot.ID,
	})
}

// receiveUpload is where the CPE's PUT actually lands — public (CPE
// traffic, no operator JWT to present), matching the firmware file-serve
// route's precedent. The body streams straight to disk; nothing is
// buffered in memory regardless of file size.
func (h *handler) receiveUpload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// This route is public (a CPE's Upload RPC PUT has no JWT), so the
	// expiring token issued with the slot is the entire authorization
	// (audit P0.3) — knowledge of a slot UUID alone is not a credential.
	if len(h.transferKey) > 0 {
		if err := transfer.Verify(h.transferKey, "upload", id, r.URL.Query().Get("token"), time.Now()); err != nil {
			http.Error(w, "missing, invalid, or expired upload token", http.StatusForbidden)
			return
		}
	}
	slot, err := h.uploads.ByID(r.Context(), id)
	if errors.Is(err, uploads.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	} else if err != nil {
		h.logger.Error("failed to look up upload slot", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if slot.Status != uploads.StatusPending {
		// One slot receives exactly one file — a second PUT (replayed
		// token, retransmit, or overwrite attempt) is rejected.
		http.Error(w, "upload slot already used", http.StatusConflict)
		return
	}

	// Cap the body (audit P0.3: unbounded stream to disk). MaxBytesReader
	// fails the read once the ceiling is crossed, so at most the ceiling
	// plus one buffer ever lands on disk before cleanup.
	body := http.MaxBytesReader(w, r.Body, h.uploadMaxBytes)
	sha256hex, size, err := h.uploadsFS.Save(id, body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			// Save already removed the partial file on the write error.
			http.Error(w, fmt.Sprintf("upload exceeds the %d-byte limit", h.uploadMaxBytes), http.StatusRequestEntityTooLarge)
			return
		}
		h.logger.Error("failed to save uploaded file", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	filename := sanitizeUploadFilename(r.Header.Get("X-Filename"))
	if filename == "" {
		filename = id
	}
	f, err := h.uploads.MarkReceived(r.Context(), id, filename, sha256hex, size)
	if errors.Is(err, uploads.ErrNotFound) {
		// Lost the PENDING->RECEIVED race to a concurrent PUT — this
		// request's file must not overwrite the recorded one.
		h.uploadsFS.Remove(id)
		http.Error(w, "upload slot already used", http.StatusConflict)
		return
	}
	if err != nil {
		h.logger.Error("failed to mark upload received", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.auditor.Record(r.Context(), "system", f.DeviceID, "UploadReceived", map[string]any{
		"upload_id": id, "file_type": f.FileType, "filename": filename, "size": size, "sha256": sha256hex,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Info("upload received", "id", id, "device_id", f.DeviceID, "filename", filename, "size", size)

	w.WriteHeader(http.StatusOK)
}

// sanitizeUploadFilename strips characters that would break out of the
// quoted Content-Disposition value it's later embedded in — this is
// CPE-supplied header data, not to be trusted verbatim.
func sanitizeUploadFilename(name string) string {
	var b []byte
	for i := 0; i < len(name) && len(b) < 200; i++ {
		c := name[i]
		if c == '"' || c == '\\' || c < 0x20 || c == 0x7f {
			continue
		}
		b = append(b, c)
	}
	return string(b)
}

func (h *handler) listDeviceUploads(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}
	list, err := h.uploads.ListByDevice(r.Context(), id)
	if err != nil {
		h.logger.Error("failed to list uploads", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	items := make([]uploadedFileResponse, 0, len(list))
	for _, f := range list {
		items = append(items, toUploadedFileResponse(&f))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// serveUploadedFile lets an operator download a received file. Unlike
// receiveUpload (CPE traffic), this is operator-authenticated — reviewing
// a vendor config backup or log file is an operator action.
func (h *handler) serveUploadedFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	f, err := h.uploads.ByID(r.Context(), id)
	if errors.Is(err, uploads.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.logger.Error("failed to look up upload", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// The upload belongs to a device; the caller must be in that
	// device's tenancy scope (audit P0.2).
	if _, ok := h.getScopedDevice(w, r, f.DeviceID); !ok {
		return
	}
	if f.Status != uploads.StatusReceived {
		http.Error(w, "file has not been received yet", http.StatusConflict)
		return
	}

	file, err := h.uploadsFS.Open(id)
	if err != nil {
		h.logger.Error("failed to open uploaded file", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	if f.Filename != nil {
		w.Header().Set("Content-Disposition", `attachment; filename="`+*f.Filename+`"`)
	}
	http.ServeContent(w, r, "", f.CreatedAt, file)
}
