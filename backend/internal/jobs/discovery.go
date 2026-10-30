package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const DiscoveryMaxObjects = 512
const DiscoveryMaxNames = 50000

func (p ParameterDiscoveryPayload) RequestPath() string {
	if p.Cursor != "" {
		return p.Cursor
	}
	return p.Root
}

// AdvanceDiscovery persists progress in the same durable job. The previous
// completed capability snapshot remains authoritative until the walk finishes.
func AdvanceDiscovery(p *ParameterDiscoveryPayload, names map[string]bool) (bool, error) {
	if p.Visited == nil {
		p.Visited = map[string]bool{}
	}
	if p.Names == nil {
		p.Names = map[string]bool{}
	}
	current := p.RequestPath()
	p.Visited[current] = true
	var objects []string
	for path, writable := range names {
		if !strings.HasPrefix(path, current) {
			return false, fmt.Errorf("discovery returned path outside requested subtree: %s", path)
		}
		if len(path) > 1024 {
			return false, fmt.Errorf("discovery path exceeds 1024 bytes")
		}
		p.Names[path] = writable
		if path != current && strings.HasSuffix(path, ".") && !p.Visited[path] {
			if strings.Count(strings.TrimPrefix(path, p.Root), ".") > 16 {
				return false, fmt.Errorf("discovery exceeds depth limit")
			}
			objects = append(objects, path)
		}
	}
	sort.Strings(objects)
	queued := map[string]bool{}
	for _, path := range p.Pending {
		queued[path] = true
	}
	for _, path := range objects {
		if !queued[path] {
			p.Pending = append(p.Pending, path)
			queued[path] = true
		}
	}
	if len(p.Visited)+len(p.Pending) > DiscoveryMaxObjects || len(p.Names) > DiscoveryMaxNames {
		return false, fmt.Errorf("discovery exceeds bounded walk limits")
	}
	if len(p.Pending) == 0 {
		return true, nil
	}
	p.Cursor, p.Pending = p.Pending[0], p.Pending[1:]
	return false, nil
}

// DiscoveryMaxSkipped bounds how many objects one walk may skip before the
// device is treated as unable to serve discovery at all.
const DiscoveryMaxSkipped = 32

// SkipDiscoveryCursor abandons the object currently being walked — the CPE
// faulted on it, or dropped the session instead of answering (observed on
// Nokia FastMile 1.2501.00.0798 for Device.DynamicDNS.Client.) — records it
// in Skipped, and advances to the next pending object. It reports done when
// nothing is left to walk. Skipping is safe because discovery is read-only;
// retrying the same object would only stall the walk forever.
func SkipDiscoveryCursor(p *ParameterDiscoveryPayload) (bool, error) {
	if p.Cursor == "" {
		return false, fmt.Errorf("discovery has no object in progress to skip")
	}
	if len(p.Skipped) >= DiscoveryMaxSkipped {
		return false, fmt.Errorf("discovery skipped more than %d objects the device could not serve", DiscoveryMaxSkipped)
	}
	if p.Visited == nil {
		p.Visited = map[string]bool{}
	}
	p.Visited[p.Cursor] = true
	p.Skipped = append(p.Skipped, p.Cursor)
	if len(p.Pending) == 0 {
		p.Cursor = ""
		return true, nil
	}
	p.Cursor, p.Pending = p.Pending[0], p.Pending[1:]
	return false, nil
}

// InFlightDiscoveries returns the device's PARAMETER_DISCOVERY jobs whose
// GetParameterNames is still awaiting a response.
func (r *Repository) InFlightDiscoveries(ctx context.Context, deviceID string) ([]*Job, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE device_id=$1 AND type=$2 AND status=$3 ORDER BY created_at`,
		deviceID, TypeParameterDiscovery, StatusRPCSent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

func (r *Repository) RequeueDiscovery(ctx context.Context, job *Job, payload ParameterDiscoveryPayload) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `UPDATE jobs SET payload=$2, status='QUEUED', updated_at=now(), lease_owner=NULL, leased_until=NULL WHERE id=$1 AND attempts=$3 AND status='RPC_SENT'`, job.ID, raw, job.Attempts)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("discovery attempt is no longer current")
	}
	return nil
}

func (r *Repository) RecordDiscoveryFallback(ctx context.Context, id, commandKey string) error {
	raw, err := json.Marshal(map[string]string{"fallback_command_key": commandKey})
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `UPDATE jobs SET result_detail=$2,updated_at=now() WHERE id=$1 AND type='PARAMETER_DISCOVERY'`, id, raw)
	return err
}
