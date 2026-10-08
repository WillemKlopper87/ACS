package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Verification exposes path-level outcomes without returning secrets or values.
type Verification struct {
	Matched    []string `json:"matched"`
	Mismatched []string `json:"mismatched"`
	Missing    []string `json:"missing"`
	Skipped    []string `json:"skipped"`
}

func SensitivePath(path string) bool {
	path = strings.ToLower(path)
	for _, word := range []string{"password", "passphrase", "preSharedKey", "secret", "token", "privatekey"} {
		if strings.Contains(path, strings.ToLower(word)) {
			return true
		}
	}
	return false
}
func VerificationPaths(p SetParameterPayload) []string {
	var paths []string
	for _, v := range p.Parameters {
		if !skipReadback(p, v.Name) {
			paths = append(paths, v.Name)
		}
	}
	return paths
}
func skipReadback(p SetParameterPayload, path string) bool {
	if SensitivePath(path) {
		return true
	}
	for _, skipped := range p.NonReadable {
		if skipped == path {
			return true
		}
	}
	return false
}
func CompareReadback(p SetParameterPayload, actual map[string]string) Verification {
	out := Verification{Matched: []string{}, Mismatched: []string{}, Missing: []string{}, Skipped: []string{}}
	for _, v := range p.Parameters {
		if skipReadback(p, v.Name) {
			out.Skipped = append(out.Skipped, v.Name)
			continue
		}
		value, ok := actual[v.Name]
		if !ok {
			out.Missing = append(out.Missing, v.Name)
			continue
		}
		expected := v.Value
		if strings.TrimPrefix(v.Type, "xsd:") == "boolean" {
			expected = normalizeBool(expected)
			value = normalizeBool(value)
		}
		if value == expected {
			out.Matched = append(out.Matched, v.Name)
		} else {
			out.Mismatched = append(out.Mismatched, v.Name)
		}
	}
	sort.Strings(out.Matched)
	sort.Strings(out.Mismatched)
	sort.Strings(out.Missing)
	sort.Strings(out.Skipped)
	return out
}
func normalizeBool(value string) string {
	if value == "1" {
		return "true"
	}
	if value == "0" {
		return "false"
	}
	return value
}
func (r *Repository) BeginReadback(ctx context.Context, job *Job, payload SetParameterPayload) error {
	payload.VerificationPending = true
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `UPDATE jobs SET payload=$2,status='QUEUED',updated_at=now(),lease_owner=NULL,leased_until=NULL WHERE id=$1 AND status='RPC_SENT' AND attempts=$3`, job.ID, raw, job.Attempts)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("write attempt is no longer current")
	}
	return nil
}

// MissingReadPaths detects partial reads without treating an empty optional
// object as a successful parameter read. Search paths are evaluated by the agent.
func MissingReadPaths(paths []string, actual map[string]string) []string {
	missing := []string{}
	for _, path := range paths {
		if strings.HasSuffix(path, ".") || strings.ContainsAny(path, "*[{(") {
			continue
		}
		if _, ok := actual[path]; !ok {
			missing = append(missing, path)
		}
	}
	return missing
}
