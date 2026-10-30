package jobs

import (
	"context"
	"encoding/json"

	"acs/internal/parameters"
)

type protectedWritesKey struct{}

// AllowProtectedWrites marks ctx for a system flow that owns a protected
// parameter (parameters.ProtectedReason): credential rotation writes the
// ACS and Connection Request credentials, XMPP provisioning the XMPP
// Connection Request settings. It is a context value, so no API input,
// template, policy, schedule or order payload can grant it. flow names
// the caller for code review; it is not stored.
func AllowProtectedWrites(ctx context.Context, flow string) context.Context {
	return context.WithValue(ctx, protectedWritesKey{}, flow)
}

func protectedWritesAllowed(ctx context.Context) bool {
	flow, _ := ctx.Value(protectedWritesKey{}).(string)
	return flow != ""
}

// checkProtectedWrite refuses a SET_PARAMETER job that writes a protected
// parameter. Every parameter write in the ACS is such a job, so this one
// check covers every path that can queue one.
func checkProtectedWrite(ctx context.Context, jobType string, payloadJSON []byte) error {
	if jobType != TypeSetParameter || protectedWritesAllowed(ctx) {
		return nil
	}
	var p SetParameterPayload
	if err := json.Unmarshal(payloadJSON, &p); err != nil {
		return nil // not a parameter list; nothing to check
	}
	names := make([]string, 0, len(p.Parameters))
	for _, w := range p.Parameters {
		names = append(names, w.Name)
	}
	return parameters.CheckProtected(names...)
}
