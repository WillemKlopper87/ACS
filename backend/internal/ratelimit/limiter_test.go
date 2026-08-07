package ratelimit

import (
	"testing"
	"time"
)

func TestLimiter_AllowsBurstThenBlocks(t *testing.T) {
	l := New(1, 3, time.Minute) // 1/sec sustained, burst of 3

	for i := 0; i < 3; i++ {
		if !l.Allow("key-a") {
			t.Fatalf("request %d within burst was denied, want allowed", i)
		}
	}
	if l.Allow("key-a") {
		t.Error("request beyond burst was allowed, want denied")
	}
}

func TestLimiter_KeysAreIndependent(t *testing.T) {
	l := New(1, 1, time.Minute)

	if !l.Allow("key-a") {
		t.Fatal("first request for key-a was denied, want allowed")
	}
	if l.Allow("key-a") {
		t.Error("second immediate request for key-a was allowed, want denied (burst exhausted)")
	}
	if !l.Allow("key-b") {
		t.Error("first request for key-b was denied, want allowed — a different key must not share key-a's bucket")
	}
}

func TestLimiter_IdleBucketsEvicted(t *testing.T) {
	l := New(1, 1, 10*time.Millisecond)

	l.Allow("key-a")
	if len(l.buckets) != 1 {
		t.Fatalf("buckets = %d after first Allow, want 1", len(l.buckets))
	}

	time.Sleep(20 * time.Millisecond)
	l.Allow("key-b") // triggers eviction sweep as a side effect

	if _, ok := l.buckets["key-a"]; ok {
		t.Error("key-a's bucket survived past idleTTL, want evicted")
	}
	if _, ok := l.buckets["key-b"]; !ok {
		t.Error("key-b's bucket missing right after its own Allow call")
	}
}
