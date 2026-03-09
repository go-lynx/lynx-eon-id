package eonId

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-lynx/lynx"
	lynxlog "github.com/go-lynx/lynx/log"
	"github.com/go-lynx/lynx/plugins"
	"github.com/redis/go-redis/v9"

	pb "github.com/go-lynx/lynx-eon-id/conf"
)

// PlugSnowflake represents an Eon-ID (Snowflake-style) generator plugin instance
type PlugSnowflake struct {
	// Inherits from base plugin
	*plugins.BasePlugin
	// Eon-ID configuration
	conf *pb.EonId
	// Redis client for worker ID registration
	redisClient redis.UniversalClient
	// Worker ID manager
	workerManager *WorkerIDManager
	// ID generator
	generator *Generator
	// Shutdown channel
	shutdownCh chan struct{}
	// Ensure shutdown channel is closed only once
	shutdownOnce sync.Once
	// Ensure stop/cleanup logic runs only once (Stop and CleanupTasks are idempotent)
	stopCleanupOnce sync.Once
	// Mutex for thread safety
	mu sync.RWMutex
	// Plugin runtime
	runtime plugins.Runtime
	// Logger instance
	logger log.Logger
}

// WorkerIDManager manages worker ID registration and heartbeat
// Uses Redis INCR for lock-free worker ID allocation
type WorkerIDManager struct {
	redisClient       redis.UniversalClient
	datacenterID      int64
	keyPrefix         string
	ttl               time.Duration
	heartbeatInterval time.Duration
	// Worker state: use registered to distinguish "not yet registered" from "registered with workerID 0"
	workerID   int64
	registered bool
	// Heartbeat lifecycle management
	heartbeatCtx     context.Context
	heartbeatCancel  context.CancelFunc
	heartbeatRunning bool
	// Registration info preserved for heartbeat
	registerTime   time.Time
	instanceID     string
	localIP        string // Local IP address for troubleshooting
	serviceName    string // Application name from lynx (e.g. betday-user)
	serviceVersion string // Application version from lynx (e.g. v1.0.0)
	// Health state - used to stop ID generation when heartbeat fails
	healthy int32 // atomic: 1=healthy, 0=unhealthy
	// Mutex for state management
	mu sync.RWMutex
}

// Generator produces 64-bit Snowflake-style unique IDs (Eon-ID).
type Generator struct {
	// Configuration
	datacenterID int64
	workerID     int64
	customEpoch  int64
	workerIDBits int64
	sequenceBits int64

	// Bit shifts
	timestampShift  int64
	datacenterShift int64
	workerShift     int64

	// Bit masks
	maxDatacenterID int64
	maxWorkerID     int64
	maxSequence     int64

	// State
	lastTimestamp int64
	sequence      int64

	// Statistics
	generatedCount     int64
	clockBackwardCount int64

	// Clock drift protection
	enableClockDriftProtection bool
	maxClockDrift              time.Duration
	clockDriftAction           string
	lastClockCheck             time.Time

	// Sequence cache for performance
	enableSequenceCache bool
	sequenceCache       []int64
	cacheIndex          int
	cacheSize           int

	// Shutdown state (isShuttingDownAtomic allows lock-free check in retry loop)
	isShuttingDown       bool
	isShuttingDownAtomic int32

	// Timestamp bits for ParseID validation (64 - timestampShift)
	timestampBits int64
	// Ignore mode: reject if lastTimestamp drifts beyond real time by this much (ms)
	maxIgnoreBackwardDriftMs int64

	// Metrics collection
	metrics *Metrics

	// Mutex for thread safety
	mu sync.Mutex
}

// Metrics holds detailed metrics for the Eon-ID generator.
type Metrics struct {
	// ID generation metrics
	IDsGenerated      int64
	ClockDriftEvents  int64
	WorkerIDConflicts int64
	SequenceOverflows int64

	// Performance metrics
	GenerationLatency time.Duration
	AverageLatency    time.Duration
	P95Latency        time.Duration
	P99Latency        time.Duration
	MaxLatency        time.Duration
	MinLatency        time.Duration

	// Cache metrics
	CacheHitRate float64
	CacheHits    int64
	CacheMisses  int64
	CacheRefills int64

	// Throughput metrics
	IDGenerationRate   float64 // IDs per second
	PeakGenerationRate float64 // Peak IDs per second

	// Error metrics
	GenerationErrors int64
	RedisErrors      int64
	TimeoutErrors    int64
	ValidationErrors int64

	// Connection metrics
	RedisConnectionPool int
	ActiveConnections   int
	IdleConnections     int

	// Timing metrics
	StartTime          time.Time
	LastGenerationTime time.Time
	UptimeDuration     time.Duration

	// Latency histogram for detailed analysis
	LatencyHistogram map[string]int64 // e.g., "0-1ms": count, "1-5ms": count

	mu sync.RWMutex
}

// ClockDriftError represents a clock drift error
type ClockDriftError struct {
	CurrentTime   time.Time
	LastTimestamp time.Time
	Drift         time.Duration
}

func (e *ClockDriftError) Error() string {
	return fmt.Sprintf("clock drift detected: current=%v, last=%v, drift=%v",
		e.CurrentTime, e.LastTimestamp, e.Drift)
}

// WorkerIDConflictError represents a worker ID conflict error
type WorkerIDConflictError struct {
	WorkerID     int64
	DatacenterID int64
	ConflictWith string
}

func (e *WorkerIDConflictError) Error() string {
	return fmt.Sprintf("worker ID conflict: worker_id=%d, datacenter_id=%d, conflict_with=%s",
		e.WorkerID, e.DatacenterID, e.ConflictWith)
}

// SID represents a generated snowflake ID with metadata
type SID struct {
	ID           int64     `json:"id"`
	Timestamp    time.Time `json:"timestamp"`
	DatacenterID int64     `json:"datacenter_id"`
	WorkerID     int64     `json:"worker_id"`
	Sequence     int64     `json:"sequence"`
}

// IDComponents represents the components of a snowflake ID
type IDComponents struct {
	Timestamp    int64 `json:"timestamp"`
	DatacenterID int64 `json:"datacenter_id"`
	WorkerID     int64 `json:"worker_id"`
	Sequence     int64 `json:"sequence"`
}

// GeneratorStats represents statistics about the generator
type GeneratorStats struct {
	WorkerID           int64 `json:"worker_id"`
	DatacenterID       int64 `json:"datacenter_id"`
	GeneratedCount     int64 `json:"generated_count"`
	ClockBackwardCount int64 `json:"clock_backward_count"`
	LastGeneratedTime  int64 `json:"last_generated_time"`
}

// Constants for default configuration
const (
	DefaultDatacenterID     = 1
	DefaultWorkerID         = 1
	DefaultTimestampBits    = 41
	DefaultDatacenterBits   = 5
	DefaultWorkerBits       = 5
	DefaultSequenceBits     = 12
	DefaultEpoch            = 1609459200000 // 2021-01-01 00:00:00 UTC in milliseconds
	DefaultMaxClockBackward = 5000          // 5 seconds in milliseconds

	// DefaultRedisKeyPrefix is the Redis worker registration key prefix; should end with ":"
	DefaultRedisKeyPrefix    = "lynx:eon-id:"
	DefaultWorkerIDTTL       = 30 * time.Second
	DefaultHeartbeatInterval = 10 * time.Second
)

const (
	// DefaultWorkerIDBits Default A bit of allocation
	DefaultWorkerIDBits = 5

	// DefaultMaxClockDrift Default timing
	DefaultMaxClockDrift      = 5 * time.Second
	DefaultClockCheckInterval = 1 * time.Second

	// DefaultSequenceCacheSize Default cache size
	DefaultSequenceCacheSize = 1000

	// WorkerIDLockKey / WorkerIDRegistryKey follow DefaultRedisKeyPrefix naming; reserved for future use
	WorkerIDLockKey     = "lynx:eon-id:lock:worker_id"
	WorkerIDRegistryKey = "lynx:eon-id:registry"

	// MaxClockBackwardWait is the maximum drift we wait for in Wait mode; larger backward returns error
	MaxClockBackwardWait = 5 * time.Second

	// ClockDriftActionWait Clock drift actions
	ClockDriftActionWait   = "wait"
	ClockDriftActionError  = "error"
	ClockDriftActionIgnore = "ignore"
)

// NewSnowflakePlugin creates a new snowflake plugin instance
func NewSnowflakePlugin() *PlugSnowflake {
	return &PlugSnowflake{
		BasePlugin: plugins.NewBasePlugin(PluginName, PluginName, PluginDescription, PluginVersion, ConfPrefix, 100),
		shutdownCh: make(chan struct{}),
	}
}

// Plugin interface implementation

// ID returns the plugin ID (same as PluginName for framework lookup).
func (p *PlugSnowflake) ID() string {
	return PluginName
}

// Description returns the plugin description
func (p *PlugSnowflake) Description() string {
	return "Eon-ID generator plugin for distributed unique ID generation"
}

// Weight returns the plugin weight for loading order
func (p *PlugSnowflake) Weight() int {
	return 100
}

// UpdateConfiguration updates the plugin configuration
func (p *PlugSnowflake) UpdateConfiguration(config interface{}) error {
	if conf, ok := config.(*pb.EonId); ok {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.conf = conf
		return nil
	}
	return fmt.Errorf("invalid configuration type for eon-id plugin")
}

// Lifecycle interface implementation (Start not overridden: same as lynx-http/lynx-grpc, use BasePlugin.Start → StartupTasks + CheckHealth)

// Stop stops the plugin (idempotent with CleanupTasks via stopCleanupOnce).
func (p *PlugSnowflake) Stop(plugin plugins.Plugin) error {
	p.shutdownOnce.Do(func() { close(p.shutdownCh) })

	p.stopCleanupOnce.Do(p.doStopCleanup)
	return nil
}

// Status returns the plugin status
func (p *PlugSnowflake) Status(plugin plugins.Plugin) plugins.PluginStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.generator != nil {
		return plugins.StatusActive
	}
	return plugins.StatusInactive
}

// LifecycleSteps interface implementation

// InitializeResources initializes plugin resources (config, Redis, worker manager, generator).
// Same pattern as lynx-http/lynx-grpc so the framework always runs this during Init phase.
func (p *PlugSnowflake) InitializeResources(rt plugins.Runtime) error {
	p.runtime = rt
	p.logger = rt.GetLogger()

	conf := &pb.EonId{}
	config := rt.GetConfig()
	if config != nil {
		if err := config.Value(ConfPrefix).Scan(conf); err != nil {
			lynxlog.Warnf("failed to load snowflake configuration: %v, using defaults", err)
			conf = &pb.EonId{
				DatacenterId:               0,
				WorkerId:                   0,
				AutoRegisterWorkerId:       true,
				RedisKeyPrefix:             DefaultRedisKeyPrefix,
				EnableClockDriftProtection: true,
				ClockDriftAction:           "wait",
				EnableSequenceCache:        true,
				SequenceCacheSize:          1000,
				EnableMetrics:              true,
				RedisPluginName:            "redis",
				RedisDb:                    0,
				CustomEpoch:                DefaultEpoch,
				WorkerIdBits:               DefaultWorkerBits,
				SequenceBits:               DefaultSequenceBits,
			}
		}
	}
	p.conf = conf

	if conf.AutoRegisterWorkerId {
		redisPluginName := conf.RedisPluginName
		if redisPluginName == "" {
			redisPluginName = "redis"
		}
		if redisResource, err := rt.GetSharedResource(redisPluginName); err == nil {
			if redisClient, ok := redisResource.(redis.UniversalClient); ok {
				p.redisClient = redisClient
				lynxlog.Infof("successfully connected to Redis plugin: %s", redisPluginName)
			} else {
				lynxlog.Warnf("Redis resource is not UniversalClient type, disabling auto worker ID registration")
				conf.AutoRegisterWorkerId = false
			}
		} else {
			lynxlog.Warnf("failed to get Redis client from plugin %s: %v, disabling auto worker ID registration", redisPluginName, err)
			conf.AutoRegisterWorkerId = false
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	keyPrefix := NormalizeKeyPrefix(conf.RedisKeyPrefix)
	ttl := DefaultWorkerIDTTL
	if conf.WorkerIdTtl != nil {
		ttl = conf.WorkerIdTtl.AsDuration()
	}
	heartbeatInterval := DefaultHeartbeatInterval
	if conf.HeartbeatInterval != nil {
		heartbeatInterval = conf.HeartbeatInterval.AsDuration()
	}

	localIP := getLocalIP()
	if localIP == "" || localIP == "unknown" {
		if h := lynx.GetHost(); h != "" {
			localIP = h
		} else if localIP == "" {
			localIP = "unknown"
		}
	}
	p.workerManager = &WorkerIDManager{
		redisClient:       p.redisClient,
		workerID:          int64(conf.WorkerId),
		datacenterID:      int64(conf.DatacenterId),
		keyPrefix:         keyPrefix,
		ttl:               ttl,
		heartbeatInterval: heartbeatInterval,
		localIP:           localIP,
		serviceName:       lynx.GetName(),
		serviceVersion:    lynx.GetVersion(),
	}
	if !conf.AutoRegisterWorkerId {
		atomic.StoreInt32(&p.workerManager.healthy, 1)
	}

	generatorConfig := &GeneratorConfig{
		CustomEpoch:                conf.CustomEpoch,
		DatacenterIDBits:           DefaultDatacenterBits,
		WorkerIDBits:               int(conf.WorkerIdBits),
		SequenceBits:               int(conf.SequenceBits),
		EnableClockDriftProtection: conf.EnableClockDriftProtection,
		MaxClockDrift:              time.Duration(5 * time.Second),
		ClockDriftAction:           conf.ClockDriftAction,
		EnableSequenceCache:        conf.EnableSequenceCache,
		SequenceCacheSize:          int(conf.SequenceCacheSize),
		EnableMetrics:              conf.EnableMetrics,
	}
	if generatorConfig.CustomEpoch == 0 {
		generatorConfig.CustomEpoch = DefaultEpoch
	}
	if generatorConfig.WorkerIDBits == 0 {
		generatorConfig.WorkerIDBits = DefaultWorkerBits
	}
	if generatorConfig.SequenceBits == 0 {
		generatorConfig.SequenceBits = DefaultSequenceBits
	}
	if generatorConfig.SequenceCacheSize == 0 {
		generatorConfig.SequenceCacheSize = 1000
	}
	if generatorConfig.ClockDriftAction == "" {
		generatorConfig.ClockDriftAction = ClockDriftActionWait
	}
	if conf.MaxClockDrift != nil {
		generatorConfig.MaxClockDrift = conf.MaxClockDrift.AsDuration()
	}

	var err error
	p.generator, err = NewSnowflakeGeneratorCore(int64(conf.DatacenterId), int64(conf.WorkerId), generatorConfig)
	if err != nil {
		return fmt.Errorf("failed to create eon-id generator: %w", err)
	}
	return nil
}

// StartupTasks performs startup tasks
func (p *PlugSnowflake) StartupTasks() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Auto-register worker ID if enabled and not already registered
	if p.conf != nil && p.conf.AutoRegisterWorkerId && p.workerManager != nil && p.redisClient != nil {
		// Check if worker ID is already set (manual configuration)
		if p.conf.WorkerId > 0 {
			// Try to register the specific worker ID
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			if err := p.workerManager.RegisterSpecificWorkerID(ctx, int64(p.conf.WorkerId)); err != nil {
				lynxlog.Warnf("failed to register specific worker ID %d: %v, trying auto-register", p.conf.WorkerId, err)
				// Fall back to auto-register
				maxWorkerID := int64((1 << p.conf.WorkerIdBits) - 1)
				if maxWorkerID == 0 {
					maxWorkerID = 31 // Default max worker ID
				}
				workerID, err := p.workerManager.RegisterWorkerID(ctx, maxWorkerID)
				if err != nil {
					return fmt.Errorf("failed to auto-register worker ID: %w", err)
				}
				// Update generator with new worker ID (thread-safe)
				if p.generator != nil {
					p.generator.mu.Lock()
					p.generator.workerID = workerID
					p.generator.mu.Unlock()
				}
				lynxlog.Infof("auto-registered worker ID: %d", workerID)
			} else {
				lynxlog.Infof("registered specific worker ID: %d", p.conf.WorkerId)
			}
		} else {
			// Auto-register worker ID
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			maxWorkerID := int64((1 << p.conf.WorkerIdBits) - 1)
			if maxWorkerID == 0 {
				maxWorkerID = 31 // Default max worker ID
			}
			workerID, err := p.workerManager.RegisterWorkerID(ctx, maxWorkerID)
			if err != nil {
				return fmt.Errorf("failed to auto-register worker ID: %w", err)
			}
			// Update generator with new worker ID (thread-safe)
			if p.generator != nil {
				p.generator.mu.Lock()
				p.generator.workerID = workerID
				p.generator.mu.Unlock()
			}
			lynxlog.Infof("auto-registered worker ID: %d", workerID)
		}
	}

	return nil
}

// doStopCleanup runs shutdown logic once; shared by Stop and CleanupTasks via stopCleanupOnce.
func (p *PlugSnowflake) doStopCleanup() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.workerManager != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.workerManager.UnregisterWorkerID(ctx); err != nil {
			lynxlog.Warnf("failed to unregister worker ID: %v", err)
		} else {
			lynxlog.Infof("unregistered worker ID")
		}
	}
	if p.generator != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.generator.Shutdown(ctx); err != nil {
			lynxlog.Warnf("failed to shutdown generator: %v", err)
		}
	}
}

// CleanupTasks performs cleanup (idempotent with Stop via doStopCleanup).
func (p *PlugSnowflake) CleanupTasks() error {
	p.shutdownOnce.Do(func() { close(p.shutdownCh) })
	p.stopCleanupOnce.Do(p.doStopCleanup)
	return nil
}

// CheckHealth checks plugin health
func (p *PlugSnowflake) CheckHealth() error {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.generator == nil {
		return fmt.Errorf("eon-id generator not initialized")
	}

	return nil
}

// GetHealth implements the HealthCheck interface (minimizes lock hold; Redis Ping runs outside lock).
func (p *PlugSnowflake) GetHealth() plugins.HealthReport {
	p.mu.RLock()
	generator := p.generator
	redisClient := p.redisClient
	workerManager := p.workerManager
	conf := p.conf

	status := "healthy"
	details := make(map[string]any)
	message := "Eon-ID generator is operating normally"

	if generator == nil {
		status = "unhealthy"
		message = "Eon-ID generator not initialized"
		details["generator_status"] = "not_initialized"
	} else {
		details["generator_status"] = "initialized"
		details["worker_id"] = generator.workerID
		details["datacenter_id"] = generator.datacenterID
		details["custom_epoch"] = generator.customEpoch
		details["generated_count"] = atomic.LoadInt64(&generator.generatedCount)
		details["clock_backward_count"] = atomic.LoadInt64(&generator.clockBackwardCount)
		details["is_shutting_down"] = generator.isShuttingDown
		if atomic.LoadInt64(&generator.clockBackwardCount) > 0 {
			status = "degraded"
			message = "Clock backward events detected"
		}
	}
	if workerManager != nil {
		details["worker_manager_status"] = "active"
		details["worker_manager_worker_id"] = workerManager.workerID
		details["worker_manager_datacenter_id"] = workerManager.datacenterID
		details["worker_manager_key_prefix"] = workerManager.keyPrefix
		details["worker_manager_ttl"] = workerManager.ttl.String()
		details["worker_manager_heartbeat_interval"] = workerManager.heartbeatInterval.String()
	} else {
		details["worker_manager_status"] = "not_configured"
	}
	if conf != nil {
		details["configuration"] = map[string]any{
			"datacenter_id":           conf.DatacenterId,
			"worker_id":               conf.WorkerId,
			"custom_epoch":            conf.CustomEpoch,
			"auto_register_worker_id": conf.AutoRegisterWorkerId,
			"redis_plugin_name":       conf.RedisPluginName,
			"redis_key_prefix":        conf.RedisKeyPrefix,
			"worker_id_ttl":           conf.WorkerIdTtl,
			"heartbeat_interval":      conf.HeartbeatInterval,
			"enable_metrics":          conf.EnableMetrics,
			"clock_drift_protection":  conf.EnableClockDriftProtection,
			"sequence_cache":          conf.EnableSequenceCache,
			"max_clock_drift":         conf.MaxClockDrift,
			"clock_check_interval":    conf.ClockCheckInterval,
			"clock_drift_action":      conf.ClockDriftAction,
			"sequence_cache_size":     conf.SequenceCacheSize,
			"redis_db":                conf.RedisDb,
			"worker_id_bits":          conf.WorkerIdBits,
			"sequence_bits":           conf.SequenceBits,
		}
	}
	p.mu.RUnlock()

	// Run Redis Ping outside lock to avoid blocking other callers.
	if redisClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := redisClient.Ping(ctx).Err(); err != nil {
			if status == "healthy" {
				status = "degraded"
			}
			message = "Redis connection issues detected"
			details["redis_status"] = "unhealthy"
			details["redis_error"] = err.Error()
		} else {
			details["redis_status"] = "healthy"
		}
	} else {
		details["redis_status"] = "not_configured"
	}

	// Read metrics outside lock (generator ref already copied; GetSnapshot uses its own lock).
	if generator != nil {
		genMetrics := generator.GetMetrics()
		if genMetrics != nil {
			snap := genMetrics.GetSnapshot()
			lastGenStr := ""
			if !snap.LastGenerationTime.IsZero() {
				lastGenStr = snap.LastGenerationTime.Format(time.RFC3339)
			}
			details["metrics"] = map[string]any{
				"ids_generated":        snap.IDsGenerated,
				"clock_drift_events":   snap.ClockDriftEvents,
				"worker_id_conflicts":  snap.WorkerIDConflicts,
				"sequence_overflows":   snap.SequenceOverflows,
				"generation_errors":    snap.GenerationErrors,
				"redis_errors":         snap.RedisErrors,
				"timeout_errors":       snap.TimeoutErrors,
				"validation_errors":    snap.ValidationErrors,
				"id_generation_rate":   snap.IDGenerationRate,
				"peak_generation_rate": snap.PeakGenerationRate,
				"uptime_duration":      snap.UptimeDuration.String(),
				"last_generation_time": lastGenStr,
			}
			totalOperations := snap.IDsGenerated + snap.GenerationErrors
			if totalOperations > 0 {
				errorRate := float64(snap.GenerationErrors) / float64(totalOperations)
				details["error_rate"] = errorRate
				if errorRate > 0.1 {
					status = "degraded"
					message = "High error rate detected"
				}
			}
		}
	}

	return plugins.HealthReport{
		Status:    status,
		Details:   details,
		Timestamp: time.Now().Unix(),
		Message:   message,
	}
}

// DependencyAware interface implementation

// RedisPluginID is the plugin ID of lynx-redis (go-lynx.plugin.redis.client.<version>).
// Used so eon-id loads after redis and can get the client via GetSharedResource("redis").
const RedisPluginID = "go-lynx.plugin.redis.client.v1.5.5"

// GetDependencies returns plugin dependencies so eon-id loads after redis and can use GetSharedResource("redis").
// When conf is nil we still declare the dependency so load order is correct; when conf has AutoRegisterWorkerId we require redis.
func (p *PlugSnowflake) GetDependencies() []plugins.Dependency {
	var deps []plugins.Dependency
	needRedis := p.conf == nil || p.conf.AutoRegisterWorkerId
	if !needRedis {
		return deps
	}
	deps = append(deps, plugins.Dependency{
		ID:          RedisPluginID,
		Name:        "Redis",
		Type:        plugins.DependencyTypeRequired,
		Required:    true,
		Description: "Redis client for worker ID management",
	})
	return deps
}

// Snowflake specific methods

// GenerateID generates a new snowflake ID
// Note: Generator.GenerateID() is internally thread-safe with its own mutex,
// so we only need to protect the nil check here to avoid double locking overhead.
func (p *PlugSnowflake) GenerateID() (int64, error) {
	// Quick nil check with read lock
	p.mu.RLock()
	generator := p.generator
	workerManager := p.workerManager
	p.mu.RUnlock()

	if generator == nil {
		return 0, fmt.Errorf("eon-id generator not initialized")
	}

	// Check worker manager health to prevent ID duplication
	// when heartbeat fails and worker ID may have been taken by another instance
	if workerManager != nil && !workerManager.IsHealthy() {
		return 0, fmt.Errorf("worker ID registration unhealthy, cannot generate ID safely")
	}

	// Generator.GenerateID() has its own mutex protection
	return generator.GenerateID()
}

// GenerateIDWithMetadata generates a new snowflake ID with metadata
func (p *PlugSnowflake) GenerateIDWithMetadata() (int64, *SID, error) {
	// Quick nil check with read lock
	p.mu.RLock()
	generator := p.generator
	workerManager := p.workerManager
	p.mu.RUnlock()

	if generator == nil {
		return 0, nil, fmt.Errorf("snowflake generator not initialized")
	}

	// Check worker manager health to prevent ID duplication
	if workerManager != nil && !workerManager.IsHealthy() {
		return 0, nil, fmt.Errorf("worker ID registration unhealthy, cannot generate ID safely")
	}

	// Generator methods have their own mutex protection
	return generator.GenerateIDWithMetadata()
}

// ParseID parses a snowflake ID into its components
func (p *PlugSnowflake) ParseID(id int64) (*SID, error) {
	// Quick nil check with read lock
	p.mu.RLock()
	generator := p.generator
	p.mu.RUnlock()

	if generator == nil {
		return nil, fmt.Errorf("snowflake generator not initialized")
	}

	// ParseID is read-only and safe
	return generator.ParseID(id)
}

// GetGenerator returns the snowflake generator instance
func (p *PlugSnowflake) GetGenerator() *Generator {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return p.generator
}
