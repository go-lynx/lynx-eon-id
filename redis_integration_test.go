package eonId

import "testing"

func TestDefaultRedisIntegrationConfig_UsesRuntimeResourceName(t *testing.T) {
	cfg := DefaultRedisIntegrationConfig()
	if cfg.RedisPluginName != RedisPluginName {
		t.Fatalf("default redis plugin name should be %q, got %q", RedisPluginName, cfg.RedisPluginName)
	}
}

func TestNewRedisIntegration_NoRuntimeResourceDoesNotCreateLocalhostClient(t *testing.T) {
	cfg := DefaultRedisIntegrationConfig()
	integration, err := NewRedisIntegration(cfg)
	if err == nil {
		if integration != nil && integration.GetClient() != nil {
			_ = integration.GetClient().Close()
		}
		t.Fatal("NewRedisIntegration should fail when no Lynx Redis shared resource is available")
	}
}
