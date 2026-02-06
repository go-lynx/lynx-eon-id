package eonId

import (
	"testing"
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
