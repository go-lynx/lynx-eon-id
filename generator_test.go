package eonId

import (
	"testing"
	"time"
)

func TestParseID_Valid(t *testing.T) {
	cfg := DefaultGeneratorConfig()
	cfg.EnableMetrics = false
	g, err := NewSnowflakeGeneratorCore(1, 1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	id, err := g.GenerateID()
	if err != nil {
		t.Fatal(err)
	}
	sid, err := g.ParseID(id)
	if err != nil {
		t.Fatalf("ParseID failed: %v", err)
	}
	if sid.DatacenterID != 1 || sid.WorkerID != 1 {
		t.Errorf("ParseID got dc=%d worker=%d", sid.DatacenterID, sid.WorkerID)
	}
	if sid.Timestamp.IsZero() {
		t.Error("ParseID timestamp is zero")
	}
}

func TestParseID_Negative(t *testing.T) {
	cfg := DefaultGeneratorConfig()
	cfg.EnableMetrics = false
	g, err := NewSnowflakeGeneratorCore(0, 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = g.ParseID(-1)
	if err == nil {
		t.Error("ParseID(-1) should error")
	}
}

func TestParseID_TimestampInRange(t *testing.T) {
	cfg := DefaultGeneratorConfig()
	cfg.EnableMetrics = false
	g, err := NewSnowflakeGeneratorCore(0, 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := g.GenerateID()
	sid, err := g.ParseID(id)
	if err != nil {
		t.Fatal(err)
	}
	ms := sid.Timestamp.UnixMilli()
	maxTs := g.customEpoch + (1<<41 - 1)
	if ms < g.customEpoch || ms > maxTs {
		t.Errorf("parsed timestamp %d outside [%d, %d]", ms, g.customEpoch, maxTs)
	}
}

func TestNewSnowflakeGeneratorCore_EnableMetricsFalse(t *testing.T) {
	cfg := DefaultGeneratorConfig()
	cfg.EnableMetrics = false
	g, err := NewSnowflakeGeneratorCore(0, 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if g.metrics != nil {
		t.Error("generator metrics should be nil when EnableMetrics is false")
	}
	_, err = g.GenerateID()
	if err != nil {
		t.Fatal(err)
	}
	// GetMetrics 应返回 nil
	if g.GetMetrics() != nil {
		t.Error("GetMetrics() should be nil when metrics disabled")
	}
}

func TestDefaultGeneratorConfig_EnableMetrics(t *testing.T) {
	cfg := DefaultGeneratorConfig()
	if !cfg.EnableMetrics {
		t.Error("DefaultGeneratorConfig should have EnableMetrics true")
	}
}

func TestClockDriftAction_Default(t *testing.T) {
	cfg := DefaultGeneratorConfig()
	if cfg.ClockDriftAction != ClockDriftActionWait {
		t.Errorf("default ClockDriftAction want %q got %q", ClockDriftActionWait, cfg.ClockDriftAction)
	}
}

func TestGenerator_GenerateID_NoMetricsNoPanic(t *testing.T) {
	cfg := DefaultGeneratorConfig()
	cfg.EnableMetrics = false
	cfg.EnableSequenceCache = false
	g, err := NewSnowflakeGeneratorCore(1, 1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		_, err := g.GenerateID()
		if err != nil {
			t.Fatalf("GenerateID: %v", err)
		}
	}
}

func TestParseID_ZeroID(t *testing.T) {
	cfg := DefaultGeneratorConfig()
	cfg.EnableMetrics = false
	g, err := NewSnowflakeGeneratorCore(0, 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// id=0 => timestamp=0, sequence=0, worker=0, dc=0 => timestamp = 0 + epoch = epoch，刚好合法
	sid, err := g.ParseID(0)
	if err != nil {
		t.Fatalf("ParseID(0) might be valid if epoch fits: %v", err)
	}
	if sid != nil && sid.Timestamp.UnixMilli() != g.customEpoch {
		t.Errorf("ParseID(0) timestamp want %d got %d", g.customEpoch, sid.Timestamp.UnixMilli())
	}
}

func TestValidateEpoch_Future(t *testing.T) {
	cfg := DefaultGeneratorConfig()
	cfg.CustomEpoch = time.Now().Add(24 * time.Hour).UnixMilli()
	err := cfg.Validate()
	if err == nil {
		t.Error("Validate should reject future epoch")
	}
}
