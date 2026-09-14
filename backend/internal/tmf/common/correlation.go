package common

import (
	"context"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

const MaxCorrelationIDLength = 128

// Correlation is propagated from a TMF northbound request into audit records
// and downstream canonical ACS order/job work.
type Correlation struct {
	RequestID  string
	ExternalID string
	CauseID    string
}

type correlationContextKey struct{}

func NewCorrelationID() string { return uuid.NewString() }

// ParseCorrelationID validates an untrusted inbound correlation identifier so
// it is safe to carry into structured logs and audit data.
func ParseCorrelationID(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if id == "" {
		return "", InvalidQuery("correlationId", "must not be empty")
	}
	if len(id) > MaxCorrelationIDLength {
		return "", InvalidQuery("correlationId", "exceeds maximum length")
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return "", InvalidQuery("correlationId", "must not contain control characters")
		}
	}
	return id, nil
}

func WithCorrelation(ctx context.Context, correlation Correlation) context.Context {
	return context.WithValue(ctx, correlationContextKey{}, correlation)
}

func CorrelationFromContext(ctx context.Context) (Correlation, bool) {
	if ctx == nil {
		return Correlation{}, false
	}
	correlation, ok := ctx.Value(correlationContextKey{}).(Correlation)
	return correlation, ok
}
