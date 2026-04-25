package eonId

import (
	"context"
	"testing"
	"time"
)

func TestNormalizeKeyPrefix_Empty(t *testing.T) {
	got := NormalizeKeyPrefix("")
	if got != DefaultRedisKeyPrefix {
		t.Errorf("NormalizeKeyPrefix(\"\") want %q got %q", DefaultRedisKeyPrefix, got)
	}
}

func TestNormalizeKeyPrefix_AlreadyWithColon(t *testing.T) {
	const in = "lynx:eon-id:"
	got := NormalizeKeyPrefix(in)
	if got != in {
		t.Errorf("NormalizeKeyPrefix(%q) want %q got %q", in, in, got)
	}
}

func TestNormalizeKeyPrefix_WithUnderscore(t *testing.T) {
	const in = "lynx_eon_id_"
	got := NormalizeKeyPrefix(in)
	if got != in {
		t.Errorf("NormalizeKeyPrefix(%q) want %q got %q", in, in, got)
	}
}

func TestNormalizeKeyPrefix_NoSuffix(t *testing.T) {
	const in = "lynx:eon-id:worker"
	got := NormalizeKeyPrefix(in)
	want := in + ":"
	if got != want {
		t.Errorf("NormalizeKeyPrefix(%q) want %q got %q", in, want, got)
	}
}

func TestNormalizeKeyPrefix_Default(t *testing.T) {
	got := NormalizeKeyPrefix(DefaultRedisKeyPrefix)
	if got != DefaultRedisKeyPrefix {
		t.Errorf("NormalizeKeyPrefix(default) want %q got %q", DefaultRedisKeyPrefix, got)
	}
}

func TestNewWorkerIDManager_NormalizesPartialConfig(t *testing.T) {
	mgr := NewWorkerIDManager(nil, 1, &WorkerManagerConfig{KeyPrefix: "eon"})
	if mgr.keyPrefix != "eon:" {
		t.Fatalf("key prefix was not normalized: %q", mgr.keyPrefix)
	}
	if mgr.ttl != DefaultWorkerIDTTL {
		t.Fatalf("ttl should default to %v, got %v", DefaultWorkerIDTTL, mgr.ttl)
	}
	if mgr.heartbeatInterval != DefaultHeartbeatInterval {
		t.Fatalf("heartbeat interval should default to %v, got %v", DefaultHeartbeatInterval, mgr.heartbeatInterval)
	}
}

func TestWorkerIDManager_RegisterRejectsInvalidInputs(t *testing.T) {
	mgr := NewWorkerIDManager(nil, 1, nil)
	if _, err := mgr.RegisterWorkerID(context.Background(), -1); err == nil {
		t.Fatal("RegisterWorkerID should reject negative max worker ID")
	}
	if mgr.IsHealthy() {
		t.Fatal("manager should be unhealthy after invalid registration input")
	}

	mgr = NewWorkerIDManager(nil, 1, nil)
	if err := mgr.RegisterSpecificWorkerID(context.Background(), -1); err == nil {
		t.Fatal("RegisterSpecificWorkerID should reject negative worker ID")
	}
	if mgr.IsHealthy() {
		t.Fatal("manager should be unhealthy after invalid specific worker ID")
	}
}

func TestWorkerIDManager_HeartbeatLoopClearsRunningState(t *testing.T) {
	mgr := NewWorkerIDManager(nil, 1, &WorkerManagerConfig{
		KeyPrefix:         "eon:",
		TTL:               time.Second,
		HeartbeatInterval: time.Hour,
	})
	ctx, cancel := context.WithCancel(context.Background())
	mgr.mu.Lock()
	mgr.heartbeatCtx = ctx
	mgr.heartbeatCancel = cancel
	mgr.heartbeatRunning = true
	mgr.mu.Unlock()

	done := make(chan struct{})
	go func() {
		mgr.heartbeatLoop(ctx)
		close(done)
	}()
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("heartbeat loop did not stop after context cancellation")
	}

	mgr.mu.RLock()
	running := mgr.heartbeatRunning
	mgr.mu.RUnlock()
	if running {
		t.Fatal("heartbeatRunning should be false after heartbeat loop exits")
	}
}
