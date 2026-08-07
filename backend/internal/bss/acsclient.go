package bss

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ErrACSUnreachable is returned when the internal ACS REST API can't be
// reached at all (design doc: this is the BSS integration guide's
// 502/ErrACSUnreachable case). Named without the reference draft's
// "ErrACSUunreachable" typo.
var ErrACSUnreachable = errors.New("internal ACS REST engine unreachable")

const defaultHTTPTimeout = 10 * time.Second

// ACSClient calls the internal ACS REST API (the same PUT/GET
// .../parameters and GET /jobs/{command_key} endpoints Phase 2 built) —
// this is the only way bssadapter ever queues work, matching build plan
// §5.1's process-boundary decision.
type ACSClient struct {
	baseURL      string
	http         *http.Client
	serviceToken string // presented as a Bearer token when cmd/api has operator JWT auth enabled — see cmd/api's withJWTAuth doc comment
}

func NewACSClient(baseURL string, timeout time.Duration, serviceToken string) *ACSClient {
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	return &ACSClient{baseURL: baseURL, http: &http.Client{Timeout: timeout}, serviceToken: serviceToken}
}

func (c *ACSClient) setAuth(req *http.Request) {
	if c.serviceToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.serviceToken)
	}
}

type setParametersRequest struct {
	Parameters []ParameterWrite `json:"parameters"`
}

type queueResponse struct {
	CommandKey string `json:"command_key"`
	Status     string `json:"status"`
}

// SetParameters calls PUT /api/v1/devices/{id}/parameters and returns the
// queued job's command_key.
func (c *ACSClient) SetParameters(ctx context.Context, deviceID string, params []ParameterWrite) (commandKey string, err error) {
	body, err := json.Marshal(setParametersRequest{Parameters: params})
	if err != nil {
		return "", fmt.Errorf("marshal parameter write: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/devices/%s/parameters", c.baseURL, deviceID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build parameter write request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrACSUnreachable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		return "", fmt.Errorf("unexpected status from ACS: %d", resp.StatusCode)
	}

	var out queueResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode ACS response: %w", err)
	}
	return out.CommandKey, nil
}

// JobStatus is the shape GET /api/v1/jobs/{command_key} returns —
// mirrored here rather than imported, for the same process-boundary
// reason as ParameterWrite above.
type JobStatus struct {
	CommandKey  string  `json:"command_key"`
	DeviceID    string  `json:"device_id"`
	Type        string  `json:"type"`
	Status      string  `json:"status"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	CompletedAt *string `json:"completed_at,omitempty"`
	FaultCode   *string `json:"fault_code,omitempty"`
	FaultString *string `json:"fault_string,omitempty"`
}

// ErrJobNotFound mirrors a 404 from the internal jobs endpoint.
var ErrJobNotFound = errors.New("job not found")

// GetJobStatus proxies GET /api/v1/jobs/{command_key} — this is what lets
// bssadapter answer Workflow C without exposing the internal ACS REST API
// directly to BSS callers (design §5.1: everything flows through the
// adapter).
func (c *ACSClient) GetJobStatus(ctx context.Context, commandKey string) (*JobStatus, error) {
	url := fmt.Sprintf("%s/api/v1/jobs/%s", c.baseURL, commandKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build job status request: %w", err)
	}
	c.setAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrACSUnreachable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrJobNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status from ACS: %d", resp.StatusCode)
	}

	var out JobStatus
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode ACS response: %w", err)
	}
	return &out, nil
}
