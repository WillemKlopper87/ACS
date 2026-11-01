package main

import (
	"net/http"
	"strings"
)

// Errors are RFC 9457 problem details that keep the older envelope keys:
//
//	{"type": "urn:acs:problem:v1:ErrNotFound", "title": "Not found", "status": 404,
//	 "detail": "...", "code": "ErrNotFound", "error": "ErrNotFound", "message": "..."}
//
// code (= error) is the stable machine-readable value; problemTitles is its
// closed list, kept in step with the ProblemCode enum in
// openapi-bssadapter.yaml by TestProblemCodesAreDeclared. The v1 in type is
// the problem contract version: a code's meaning never changes within it.
// A client that sends Accept: application/problem+json gets that media type;
// others get application/json with the same body.
const problemTypePrefix = "urn:acs:problem:v1:"

var problemTitles = map[string]string{
	"ErrInvalidRequest":         "Invalid request",
	"ErrNotFound":               "Not found",
	"ErrInternal":               "Internal error",
	"ErrACSUnreachable":         "ACS unreachable",
	"ErrDeviceNotMapped":        "Device not found",
	"ErrDeviceNotFound":         "Device not found",
	"ErrUnsupportedAction":      "Unsupported action",
	"ErrUnauthorized":           "Unauthorized",
	"ErrJobNotFound":            "Job not found",
	"ErrInvalidRelation":        "Invalid relation",
	"ErrConflict":               "Conflict",
	"ErrServiceTerminated":      "Service terminated",
	"ErrServiceNotActive":       "Service not active",
	"ErrRoleAlreadyAssigned":    "Role already assigned",
	"ErrDeviceAlreadyAssigned":  "Device already assigned",
	"ErrReplayNeedsCustomer":    "Replay needs a customer",
	"ErrRateLimited":            "Rate limited",
	"ErrPreProvisionRefused":    "Pre-provisioning refused",
	"ErrIdempotencyKeyReused":   "Idempotency key reused",
	"ErrIdempotencyKeyInFlight": "Idempotency key in flight",
	"ErrSaleWrongState":         "Sale in the wrong state",
	"ErrGlobalClientsDisabled":  "Global clients disabled",
	"ErrClientNotCustomerBound": "Client not customer-bound",
	// OAuth 2.0 (RFC 6749 §5.2, RFC 6750 §3.1) codes keep their spelling.
	"invalid_token":          "Invalid token",
	"insufficient_scope":     "Insufficient scope",
	"invalid_request":        "Invalid request",
	"invalid_client":         "Invalid client",
	"invalid_scope":          "Invalid scope",
	"unsupported_grant_type": "Unsupported grant type",
	"server_error":           "Server error",
}

type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
	Code   string `json:"code"`
	// The older envelope (guide §4), kept for existing integrations.
	Error   string `json:"error"`
	Message string `json:"message"`
}

func newProblem(status int, code, message string) problem {
	title := problemTitles[code]
	if title == "" {
		title = http.StatusText(status)
	}
	return problem{Type: problemTypePrefix + code, Title: title, Status: status, Detail: message, Code: code, Error: code, Message: message}
}

// withProblemMediaType serves error bodies as application/problem+json to
// clients that ask for it.
func withProblemMediaType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Accept"), "application/problem+json") {
			w = &problemWriter{ResponseWriter: w}
		}
		next.ServeHTTP(w, r)
	})
}

type problemWriter struct{ http.ResponseWriter }

func (p *problemWriter) WriteHeader(status int) {
	if status >= 400 && strings.HasPrefix(p.Header().Get("Content-Type"), "application/json") {
		p.Header().Set("Content-Type", "application/problem+json")
	}
	p.ResponseWriter.WriteHeader(status)
}

func (p *problemWriter) Unwrap() http.ResponseWriter { return p.ResponseWriter }
