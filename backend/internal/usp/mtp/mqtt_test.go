package mtp

import (
	"context"
	"log/slog"
	"net"
	"testing"
	"time"
)

func TestMQTTPlaintextRequiresOptIn(t *testing.T) {
	if _, err := NewMQTT(MQTTConfig{Addr: "127.0.0.1:0", ControllerTopic: "/usp/controller"}, slog.Default()); err == nil {
		t.Error("NewMQTT with no TLS and no AllowPlaintext succeeded; plaintext must be opt-in")
	}
}

func TestMQTTStartStop(t *testing.T) {
	m, err := NewMQTT(MQTTConfig{Addr: "127.0.0.1:0", ControllerTopic: "/usp/controller", AllowPlaintext: true}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx, newRecordingHandler()); err != nil {
		t.Fatal(err)
	}
	if m.Kind() != KindMQTT {
		t.Errorf("Kind() = %q, want MQTT", m.Kind())
	}
	// The broker must be listening: a raw TCP connect succeeds.
	c, err := net.DialTimeout("tcp", m.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("broker not listening on %s: %v", m.Addr(), err)
	}
	c.Close()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopCancel()
	if err := m.Stop(stopCtx); err != nil {
		t.Errorf("Stop: %v", err)
	}
}
