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
