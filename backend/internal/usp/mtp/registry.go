package mtp

import (
	"sync"

	"acs/internal/usp"
)

// Registry tracks the one live Conn per endpoint id across every
// Transport a controller is running (R-WS.4, generalised to MQTT
// alongside WebSocket). An agent reachable over more than one MTP
// still gets a single entry: whichever Conn Add saw last.
type Registry struct {
	mu    sync.Mutex
	conns map[usp.EndpointID]Conn
}

// NewRegistry returns an empty Registry ready for concurrent use.
func NewRegistry() *Registry {
	return &Registry{conns: make(map[usp.EndpointID]Conn)}
}

// Add records c as the live connection for its endpoint id, replacing
// whatever was there. The previous Conn, if any, is returned so the
// caller can close it with a reason -- Add itself never closes
// anything, since deciding the reason (reconnect, replaced by a
// different MTP, ...) is the caller's job.
func (r *Registry) Add(c Conn) (replaced Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := c.Endpoint()
	old := r.conns[id]
	r.conns[id] = c
	return old
}

// Remove deletes c from the registry, but only if c is still the conn
// on record for its endpoint id. A disconnect callback for a Conn that
// has already been replaced by a newer one is therefore a no-op: it
// reports false and leaves the live replacement in place, so a slow
// close on the old socket cannot evict the connection that superseded
// it.
func (r *Registry) Remove(c Conn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := c.Endpoint()
	existing, ok := r.conns[id]
	if !ok || existing != c {
		return false
	}
	delete(r.conns, id)
	return true
}

// Get returns the live conn for id, or (nil, false) if none is
// registered.
func (r *Registry) Get(id usp.EndpointID) (Conn, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.conns[id]
	return c, ok
}

// Len reports how many endpoints currently have a live conn.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.conns)
}

// Each calls fn once per live conn. The conn list is copied under the
// lock and fn is called outside it, so a callback that itself calls
// Remove or Close on a conn -- the common case, since closing a conn
// typically fires a disconnect callback that removes it -- cannot
// deadlock against Each's own lock.
func (r *Registry) Each(fn func(Conn)) {
	r.mu.Lock()
	snapshot := make([]Conn, 0, len(r.conns))
	for _, c := range r.conns {
		snapshot = append(snapshot, c)
	}
	r.mu.Unlock()

	for _, c := range snapshot {
		fn(c)
	}
}
