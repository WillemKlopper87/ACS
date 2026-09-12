package mtp

import (
	"context"
	"sync"
	"testing"

	"acs/internal/usp"
)

// fakeConn is the minimal Conn a registry test needs. It records whether
// Close was called and with what reason, which is how a caller is
// expected to treat the conn Add hands back on replacement.
type fakeConn struct {
	id     usp.EndpointID
	kind   Kind
	closed string
	mu     sync.Mutex
}

func (f *fakeConn) Endpoint() usp.EndpointID           { return f.id }
func (f *fakeConn) Kind() Kind                         { return f.kind }
func (f *fakeConn) RemoteAddr() string                 { return "test" }
func (f *fakeConn) Send(context.Context, []byte) error { return nil }
func (f *fakeConn) Close(reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = reason
	return nil
}

const agentA = usp.EndpointID("os::012345-AAAA")

func TestRegistryReplaceOnReconnect(t *testing.T) {
	r := NewRegistry()
	first := &fakeConn{id: agentA, kind: KindWebSocket}
	if old := r.Add(first); old != nil {
		t.Fatalf("first Add returned a replaced conn %v, want nil", old)
	}
	second := &fakeConn{id: agentA, kind: KindMQTT}
	old := r.Add(second)
	if old != first {
		t.Fatalf("second Add returned %v, want the first conn so the caller can close it", old)
	}
	got, ok := r.Get(agentA)
	if !ok || got != second {
		t.Errorf("Get = (%v, %v), want the replacement", got, ok)
	}
	if r.Len() != 1 {
		t.Errorf("Len = %d, want 1: one conn per endpoint id", r.Len())
	}
}

// A disconnect callback from a connection that has already been replaced
// must not evict its replacement -- otherwise a slow close on the old
// socket knocks the live one out of the registry.
func TestRegistryStaleRemoveIsNoop(t *testing.T) {
	r := NewRegistry()
	first := &fakeConn{id: agentA, kind: KindWebSocket}
	second := &fakeConn{id: agentA, kind: KindWebSocket}
	r.Add(first)
	r.Add(second)
	if removed := r.Remove(first); removed {
		t.Error("Remove(first) reported true, but first was already replaced and must be a no-op")
	}
	if got, ok := r.Get(agentA); !ok || got != second {
		t.Errorf("after stale Remove, Get = (%v, %v), want the live replacement", got, ok)
	}
	if removed := r.Remove(second); !removed {
		t.Error("Remove(second) reported false for the live conn")
	}
	if _, ok := r.Get(agentA); ok {
		t.Error("conn still present after removing the live one")
	}
}

func TestRegistryGetUnknown(t *testing.T) {
	r := NewRegistry()
	if c, ok := r.Get("os::nobody-here"); ok || c != nil {
		t.Errorf("Get on unknown id = (%v, %v), want (nil, false)", c, ok)
	}
}

func TestRegistryConcurrent(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := usp.EndpointID("os::012345-" + string(rune('A'+i%5)))
			c := &fakeConn{id: id, kind: KindWebSocket}
			if old := r.Add(c); old != nil {
				_ = old.Close("replaced")
			}
			r.Get(id)
			r.Each(func(Conn) {})
			r.Remove(c)
		}(i)
	}
	wg.Wait()
}
