package eonId

import (
	"context"
	"fmt"
	"strings"

	lynxlog "github.com/go-lynx/lynx/log"
	"github.com/redis/go-redis/v9"

	pb "github.com/go-lynx/lynx-eon-id/conf"
)

// RedisIntegration handles integration with Redis plugin
type RedisIntegration struct {
	client redis.UniversalClient
	config *RedisIntegrationConfig
}

// RedisIntegrationConfig holds Redis integration configuration
type RedisIntegrationConfig struct {
	RedisPluginName string // Name of the Redis plugin instance to use
	Database        int    // Redis database number for snowflake
	KeyPrefix       string // Key prefix for snowflake keys
}

// Validate validates the Redis integration configuration
func (c *RedisIntegrationConfig) Validate() error {
	// Validate Redis plugin name
	if c.RedisPluginName == "" {
		return fmt.Errorf("redis plugin name cannot be empty")
	}

	// Validate plugin name format
	if len(c.RedisPluginName) > 100 {
		return fmt.Errorf("redis plugin name is too long (>100 chars): %s", c.RedisPluginName)
	}

	// Check for invalid characters in plugin name
	if strings.ContainsAny(c.RedisPluginName, " \t\n\r/\\") {
		return fmt.Errorf("redis plugin name contains invalid characters: %s", c.RedisPluginName)
	}

	// Validate database number
	if c.Database < 0 || c.Database > 15 {
		return fmt.Errorf("redis database number must be between 0 and 15, got %d", c.Database)
	}

	// Validate key prefix
	if c.KeyPrefix == "" {
		return fmt.Errorf("key prefix cannot be empty")
	}

	// Check key prefix length
	if len(c.KeyPrefix) > 50 {
		return fmt.Errorf("key prefix is too long (>50 chars): %s", c.KeyPrefix)
	}

	// Check for invalid characters in key prefix
	if strings.ContainsAny(c.KeyPrefix, " \t\n\r") {
		return fmt.Errorf("key prefix cannot contain whitespace characters: %s", c.KeyPrefix)
	}

	// Check for Redis key pattern conflicts
	if strings.Contains(c.KeyPrefix, "*") || strings.Contains(c.KeyPrefix, "?") {
		return fmt.Errorf("key prefix cannot contain Redis pattern characters (* or ?): %s", c.KeyPrefix)
	}

	// Prefix may omit trailing ":"; NormalizeKeyPrefix will add it when building keys.

	return nil
}

// NewRedisIntegration creates a new Redis integration
func NewRedisIntegration(config *RedisIntegrationConfig) (*RedisIntegration, error) {
	if config == nil {
		config = DefaultRedisIntegrationConfig()
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}

	// Get Redis client from Redis plugin
	client, err := getRedisClientFromPlugin(config.RedisPluginName, config.Database)
	if err != nil {
		return nil, fmt.Errorf("failed to get Redis client: %w", err)
	}

	return &RedisIntegration{
		client: client,
		config: config,
	}, nil
}

// GetClient returns the Redis client
func (r *RedisIntegration) GetClient() redis.UniversalClient {
	return r.client
}

// TestConnection tests the Redis connection
func (r *RedisIntegration) TestConnection(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

// CreateWorkerManager creates a worker ID manager using this Redis integration
func (r *RedisIntegration) CreateWorkerManager(datacenterID int64, config *WorkerManagerConfig) *WorkerIDManager {
	if config == nil {
		config = DefaultWorkerManagerConfig()
	}

	if config.KeyPrefix == "" {
		config.KeyPrefix = r.config.KeyPrefix
	}
	config.KeyPrefix = NormalizeKeyPrefix(config.KeyPrefix)

	return NewWorkerIDManager(r.client, datacenterID, config)
}

// getRedisClientFromPlugin gets Redis client from the Redis plugin
func getRedisClientFromPlugin(pluginName string, database int) (redis.UniversalClient, error) {
	if client := tryGetFromGlobalRegistry(pluginName, database); client != nil {
		return client, nil
	}

	return nil, fmt.Errorf("redis plugin resource %q not found or not initialized", pluginName)
}

type redisUniversalClientProvider interface {
	UniversalClient(context.Context) (redis.UniversalClient, error)
}

// tryGetFromGlobalRegistry attempts to get Redis client from Lynx runtime shared resources.
func tryGetFromGlobalRegistry(pluginName string, database int) redis.UniversalClient {
	app := currentLynxApp()
	if app == nil || app.GetPluginManager() == nil || app.GetPluginManager().GetRuntime() == nil {
		return nil
	}

	rt := app.GetPluginManager().GetRuntime()
	candidates := []string{pluginName, RedisPluginName, RedisLegacyResourceName, "redis.provider", RedisPluginName + ".provider"}
	seen := make(map[string]struct{}, len(candidates))
	for _, name := range candidates {
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		resource, err := rt.GetSharedResource(name)
		if err != nil {
			continue
		}
		switch v := resource.(type) {
		case redis.UniversalClient:
			return v
		case redisUniversalClientProvider:
			client, err := v.UniversalClient(context.Background())
			if err == nil {
				return client
			}
		}
	}

	return nil
}

// DefaultRedisIntegrationConfig returns default Redis integration configuration
func DefaultRedisIntegrationConfig() *RedisIntegrationConfig {
	return &RedisIntegrationConfig{
		RedisPluginName: RedisPluginName, // Default Redis plugin shared resource name
		Database:        0,               // Default Redis database
		KeyPrefix:       DefaultRedisKeyPrefix,
	}
}

// RedisSnowflakePlugin is the eon-id plugin with Redis integration, independent of the main Lynx runtime.
// Use for standalone processes or tests; in production prefer registering eon-id via Lynx and getting Redis from runtime.GetSharedResource.
type RedisSnowflakePlugin struct {
	*PlugSnowflake
	redisIntegration *RedisIntegration
	workerManager    *WorkerIDManager
}

// NewRedisSnowflakePlugin creates a plugin instance with Redis (independent of Lynx; for standalone or test use only).
func NewRedisSnowflakePlugin(config *pb.EonId, redisConfig *RedisIntegrationConfig) (*RedisSnowflakePlugin, error) {
	// Create Redis integration
	redisIntegration, err := NewRedisIntegration(redisConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Redis integration: %w", err)
	}

	// Test Redis connection
	ctx := context.Background()
	if err := redisIntegration.TestConnection(ctx); err != nil {
		return nil, fmt.Errorf("redis connection test failed: %w", err)
	}

	// Create base snowflake plugin
	basePlugin := NewSnowflakePlugin()

	// Set the configuration directly (UpdateConfiguration is the available method)
	if err := basePlugin.UpdateConfiguration(config); err != nil {
		return nil, fmt.Errorf("failed to configure snowflake plugin: %w", err)
	}

	// Create worker manager if Redis integration is enabled
	var workerManager *WorkerIDManager
	if config.GetAutoRegisterWorkerId() && config.GetRedisPluginName() != "" {
		workerManagerConfig := &WorkerManagerConfig{
			KeyPrefix:         config.GetRedisKeyPrefix(),
			TTL:               config.GetWorkerIdTtl().AsDuration(),
			HeartbeatInterval: config.GetHeartbeatInterval().AsDuration(),
		}
		workerManager = redisIntegration.CreateWorkerManager(int64(config.GetDatacenterId()), workerManagerConfig)

		// Auto-register worker ID if enabled
		if config.GetAutoRegisterWorkerId() {
			maxWorkerID := int64((1 << 10) - 1) // Default worker ID bits
			workerID, err := workerManager.RegisterWorkerID(ctx, maxWorkerID)
			if err != nil {
				return nil, fmt.Errorf("failed to auto-register worker ID: %w", err)
			}

			lynxlog.Infof("Auto-registered worker ID: %d", workerID)
		}
	}

	return &RedisSnowflakePlugin{
		PlugSnowflake:    basePlugin,
		redisIntegration: redisIntegration,
		workerManager:    workerManager,
	}, nil
}

// GetWorkerManager returns the worker manager
func (r *RedisSnowflakePlugin) GetWorkerManager() *WorkerIDManager {
	return r.workerManager
}

// GetRedisIntegration returns the Redis integration
func (r *RedisSnowflakePlugin) GetRedisIntegration() *RedisIntegration {
	return r.redisIntegration
}

// Shutdown gracefully shuts down the plugin
func (r *RedisSnowflakePlugin) Shutdown(ctx context.Context) error {
	if r.workerManager != nil {
		if err := r.workerManager.UnregisterWorkerID(ctx); err != nil {
			lynxlog.Errorf("Failed to unregister worker ID: %v", err)
			return err
		}
	}
	return nil
}

// RegisterWorkerID manually registers a specific worker ID
func (r *RedisSnowflakePlugin) RegisterWorkerID(ctx context.Context, workerID int64) error {
	if r.workerManager == nil {
		return fmt.Errorf("worker manager not initialized")
	}

	if err := r.workerManager.RegisterSpecificWorkerID(ctx, workerID); err != nil {
		return err
	}

	// Update the generator's worker ID
	r.PlugSnowflake.conf.WorkerId = int32(workerID)
	return nil
}

// GetRegisteredWorkers returns all registered workers
func (r *RedisSnowflakePlugin) GetRegisteredWorkers(ctx context.Context) ([]WorkerInfo, error) {
	if r.workerManager == nil {
		return nil, fmt.Errorf("worker manager not initialized")
	}

	return r.workerManager.GetRegisteredWorkers(ctx)
}
