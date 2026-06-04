package eonId

import (
	"context"
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-lynx/lynx/log"
	"github.com/go-lynx/lynx/pkg/timex"
	"github.com/redis/go-redis/v9"
)

// redisResultToInt64 converts Redis Lua script result to int64 to avoid panic when the client returns float64.
func redisResultToInt64(v any) (int64, error) {
	switch n := v.(type) {
	case int64:
		return n, nil
	case float64:
		return int64(n), nil
	case int:
		return int64(n), nil
	default:
		return 0, fmt.Errorf("unexpected Lua result type: %T", v)
	}
}

// getLocalIP returns the local IP address of the machine
func getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "unknown"
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			if ipNet.IP.To4() != nil {
				return ipNet.IP.String()
			}
		}
	}
	return "unknown"
}

// NewWorkerIDManager creates a new worker ID manager
// Uses Redis INCR for lock-free worker ID allocation
func NewWorkerIDManager(redisClient redis.UniversalClient, datacenterID int64, config *WorkerManagerConfig) *WorkerIDManager {
	if config == nil {
		config = DefaultWorkerManagerConfig()
	}
	keyPrefix := NormalizeKeyPrefix(config.KeyPrefix)
	ttl := config.TTL
	if ttl <= 0 {
		ttl = DefaultWorkerIDTTL
	}
	heartbeatInterval := config.HeartbeatInterval
	if heartbeatInterval <= 0 {
		heartbeatInterval = DefaultHeartbeatInterval
	}

	mgr := &WorkerIDManager{
		redisClient:       redisClient,
		datacenterID:      datacenterID,
		keyPrefix:         keyPrefix,
		ttl:               ttl,
		heartbeatInterval: heartbeatInterval,
		workerID:          -1, // Not assigned yet
		heartbeatCtx:      nil,
		heartbeatCancel:   nil,
		heartbeatRunning:  false,
		localIP:           getLocalIP(),
		serviceName:       config.ServiceName,
		serviceVersion:    config.ServiceVersion,
	}
	atomic.StoreInt32(&mgr.healthy, 1) // Initially healthy
	return mgr
}

// RegisterWorkerID registers a worker ID
// Flow: INCR to get workerID -> if exceeds max, reset to 0 -> SetNX to verify -> retry until full cycle
// Heartbeat maintains key TTL to ensure worker ID exclusivity during instance lifetime
func (w *WorkerIDManager) RegisterWorkerID(ctx context.Context, maxWorkerID int64) (int64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if maxWorkerID < 0 {
		atomic.StoreInt32(&w.healthy, 0)
		return -1, fmt.Errorf("max worker ID must be non-negative, got %d", maxWorkerID)
	}
	if w.redisClient == nil {
		atomic.StoreInt32(&w.healthy, 0)
		return -1, fmt.Errorf("redis client is nil")
	}
	if atomic.LoadInt32(&w.shuttingDown) == 1 {
		atomic.StoreInt32(&w.healthy, 0)
		return -1, fmt.Errorf("worker manager is shutting down")
	}
	if w.registered {
		return w.workerID, nil // Already registered (workerID can be 0)
	}

	// Preserve maxWorkerID so the heartbeat path can acquire a fresh worker ID via a
	// full re-registration if the current one is later taken/expired.
	w.maxWorkerID = maxWorkerID

	counterKey := w.getCounterKey()
	totalWorkerIDs := maxWorkerID + 1 // Total available worker IDs (0 to maxWorkerID)
	maxRetries := int(totalWorkerIDs) // Try each worker ID at most once (full cycle)

	// Loop to try acquiring an available worker ID
	for retryCount := 0; retryCount < maxRetries; retryCount++ {
		// Check if context is cancelled
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		default:
		}

		// 1. Atomic INCR with auto-reset using Lua script
		// If counter exceeds max, reset to 1 and return 1
		// This prevents race condition when multiple instances try to reset simultaneously
		result, err := w.redisClient.Eval(ctx, LuaScriptIncrWithReset, []string{counterKey}, totalWorkerIDs).Result()
		if err != nil {
			return -1, fmt.Errorf("failed to execute INCR script: %w", err)
		}
		seq, err := redisResultToInt64(result)
		if err != nil {
			return -1, fmt.Errorf("INCR script result: %w", err)
		}

		// 2. workerID = seq - 1 (0-based)
		workerID := seq - 1

		// 4. SetNX to verify this worker ID is available
		now := time.Now()
		w.instanceID = w.generateInstanceID()
		workerInfo := WorkerInfo{
			WorkerID:       workerID,
			DatacenterID:   w.datacenterID,
			IP:             w.localIP,
			ServiceName:    w.serviceName,
			ServiceVersion: w.serviceVersion,
			RegisterTime:   now.Unix(),
			LastHeartbeat:  now.Unix(),
			InstanceID:     w.instanceID,
		}

		key := w.getWorkerKey(workerID)
		success, err := w.redisClient.SetNX(ctx, key, workerInfo.String(), w.ttl).Result()
		if err != nil {
			return -1, fmt.Errorf("failed to SetNX worker ID %d: %w", workerID, err)
		}

		if success {
			// Registration successful
			w.workerID = workerID
			w.registered = true
			w.registerTime = now
			w.setLeaseDeadline(now)

			// Add to registry set (for monitoring)
			registryKey := w.getRegistryKey()
			_ = w.redisClient.SAdd(ctx, registryKey, fmt.Sprintf("%d:%d", w.datacenterID, workerID))

			// Push the (possibly fresh) worker ID into the generator BEFORE marking the
			// worker healthy and starting the heartbeat, so no ID is ever generated with a
			// stale worker id once health is restored. Captured under w.mu (held here).
			if onChange := w.onWorkerIDChange; onChange != nil {
				onChange(workerID)
			}

			promWorkerIDGauge.Set(float64(workerID))
			w.startHeartbeatLocked()         // Start heartbeat to maintain key TTL
			atomic.StoreInt32(&w.healthy, 1) // Mark healthy only after generator is updated

			log.Infof("successfully registered worker ID %d (datacenter: %d, attempts: %d)", workerID, w.datacenterID, retryCount+1)
			return workerID, nil
		}

		// SetNX failed, this worker ID is already taken
		log.Debugf("worker ID %d is taken, attempt %d/%d", workerID, retryCount+1, maxRetries)

		// Backoff sleep: random 10-50ms to prevent retry storms.
		backoff := timex.RandomDuration(10*time.Millisecond, 50*time.Millisecond)
		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return -1, ctx.Err()
		}
	}

	// All worker IDs are taken after a full cycle
	atomic.StoreInt32(&w.healthy, 0)
	promWorkerIDFailuresTotal.Inc()
	return -1, fmt.Errorf("all %d worker IDs are occupied, registration failed", totalWorkerIDs)
}

// RegisterSpecificWorkerID registers a specific worker ID
// Uses SetNX to verify worker ID availability, returns error if already taken
func (w *WorkerIDManager) RegisterSpecificWorkerID(ctx context.Context, workerID int64) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if workerID < 0 {
		atomic.StoreInt32(&w.healthy, 0)
		return fmt.Errorf("worker ID must be non-negative, got %d", workerID)
	}
	if w.redisClient == nil {
		atomic.StoreInt32(&w.healthy, 0)
		return fmt.Errorf("redis client is nil")
	}
	if w.registered {
		if w.workerID == workerID {
			return nil // Already registered with this ID
		}
		return fmt.Errorf("already registered with worker ID %d", w.workerID)
	}

	now := time.Now()
	w.instanceID = w.generateInstanceID()
	workerInfo := WorkerInfo{
		WorkerID:       workerID,
		DatacenterID:   w.datacenterID,
		IP:             w.localIP,
		ServiceName:    w.serviceName,
		ServiceVersion: w.serviceVersion,
		RegisterTime:   now.Unix(),
		LastHeartbeat:  now.Unix(),
		InstanceID:     w.instanceID,
	}

	// SetNX to verify worker ID availability
	key := w.getWorkerKey(workerID)
	success, err := w.redisClient.SetNX(ctx, key, workerInfo.String(), w.ttl).Result()
	if err != nil {
		return fmt.Errorf("failed to SetNX worker ID %d: %w", workerID, err)
	}
	if !success {
		atomic.StoreInt32(&w.healthy, 0) // Mark as unhealthy
		return &WorkerIDConflictError{
			WorkerID:     workerID,
			DatacenterID: w.datacenterID,
			ConflictWith: "another instance",
		}
	}

	w.workerID = workerID
	w.registered = true
	w.registerTime = now
	w.setLeaseDeadline(now)

	registryKey := w.getRegistryKey()
	_ = w.redisClient.SAdd(ctx, registryKey, fmt.Sprintf("%d:%d", w.datacenterID, workerID))

	atomic.StoreInt32(&w.healthy, 1)
	promWorkerIDGauge.Set(float64(workerID))
	w.startHeartbeatLocked()

	log.Infof("successfully registered specific worker ID %d (datacenter: %d)", workerID, w.datacenterID)
	return nil
}

// setLeaseDeadline records the monotonic lease deadline as now+ttl. Called on every
// successful register/heartbeat so IsHealthy can refuse generation before the worker
// key TTL expires (and another instance could grab the same worker ID).
func (w *WorkerIDManager) setLeaseDeadline(now time.Time) {
	atomic.StoreInt64(&w.leaseDeadline, now.Add(w.ttl).UnixNano())
}

// IsHealthy returns whether the worker manager is healthy (heartbeat is working).
// Health requires BOTH the healthy flag (heartbeat has not failed) AND that we are
// still within the lease window (now <= leaseDeadline). The lease gate is proactive:
// even if a beat has not yet been observed to fail, once the lease deadline passes the
// worker key may have expired and been taken by another instance, so generation must
// stop immediately to avoid cross-instance duplicate IDs.
func (w *WorkerIDManager) IsHealthy() bool {
	if atomic.LoadInt32(&w.healthy) != 1 {
		return false
	}
	deadline := atomic.LoadInt64(&w.leaseDeadline)
	if deadline != 0 && time.Now().UnixNano() > deadline {
		// Lease expired without a successful renewal in time; treat as unhealthy now.
		atomic.StoreInt32(&w.healthy, 0)
		return false
	}
	return true
}

// startHeartbeatLocked starts the heartbeat if not running.
// Caller must hold w.mu.
func (w *WorkerIDManager) startHeartbeatLocked() {
	if w.heartbeatRunning {
		return
	}
	// Cancel any previous context just in case
	if w.heartbeatCancel != nil {
		w.heartbeatCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.heartbeatCtx = ctx
	w.heartbeatCancel = cancel
	w.heartbeatRunning = true
	go w.heartbeatLoop(ctx)
}

// heartbeatLoop starts the heartbeat process with context cancellation.
func (w *WorkerIDManager) heartbeatLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("eon-id heartbeat loop recovered from panic: %v", r)
			atomic.StoreInt32(&w.healthy, 0)
		}
	}()
	ticker := time.NewTicker(w.heartbeatInterval)
	defer ticker.Stop()
	defer func() {
		w.mu.Lock()
		if w.heartbeatCtx == ctx {
			w.heartbeatCtx = nil
			w.heartbeatCancel = nil
			w.heartbeatRunning = false
		}
		w.mu.Unlock()
	}()

	consecutiveFailures := 0
	maxConsecutiveFailures := 3

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.sendHeartbeat(); err != nil {
				consecutiveFailures++
				promWorkerIDHeartbeatFailuresTotal.Inc()
				log.Warnf("eon-id worker heartbeat failed (attempt %d/%d): %v",
					consecutiveFailures, maxConsecutiveFailures, err)

				// Mark as unhealthy after first failure to prevent ID generation
				if consecutiveFailures >= 1 {
					atomic.StoreInt32(&w.healthy, 0)
				}

				// If too many failures, try to re-register
				if consecutiveFailures >= maxConsecutiveFailures {
					log.Errorf("eon-id worker heartbeat failed %d times, attempting re-registration",
						consecutiveFailures)

					// Try to re-register the same worker ID
					if reregErr := w.tryReRegister(ctx); reregErr != nil {
						log.Errorf("failed to re-register worker ID: %v", reregErr)
					} else {
						log.Infof("successfully re-registered worker ID %d", w.workerID)
						atomic.StoreInt32(&w.healthy, 1)
						consecutiveFailures = 0
					}
				}
			} else {
				// Reset failure counter and mark healthy on success
				if consecutiveFailures > 0 {
					log.Infof("eon-id worker heartbeat recovered after %d failures", consecutiveFailures)
					consecutiveFailures = 0
				}
				atomic.StoreInt32(&w.healthy, 1)
			}
		}
	}
}

// tryReRegister attempts to re-register the current worker ID.
// Only updates Redis if the key still belongs to this instance (same instance_id);
// avoids overwriting another instance that took the same worker ID after expiry.
// On failure, clears local state so caller can attempt full re-registration.
func (w *WorkerIDManager) tryReRegister(ctx context.Context) error {
	if w.redisClient == nil {
		return fmt.Errorf("redis client is nil")
	}

	w.mu.RLock()
	workerID := w.workerID
	registered := w.registered
	registerTime := w.registerTime
	instanceID := w.instanceID
	localIP := w.localIP
	serviceName := w.serviceName
	serviceVersion := w.serviceVersion
	w.mu.RUnlock()

	if !registered || workerID < 0 {
		return fmt.Errorf("no worker ID to re-register")
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	key := w.getWorkerKey(workerID)
	workerInfo := WorkerInfo{
		WorkerID:       workerID,
		DatacenterID:   w.datacenterID,
		IP:             localIP,
		ServiceName:    serviceName,
		ServiceVersion: serviceVersion,
		RegisterTime:   registerTime.Unix(),
		LastHeartbeat:  time.Now().Unix(),
		InstanceID:     instanceID,
	}

	result, err := w.redisClient.Eval(timeoutCtx, LuaScriptHeartbeat, []string{key},
		workerInfo.String(), instanceID, int64(w.ttl.Seconds())).Result()
	if err != nil {
		return fmt.Errorf("re-register script failed: %w", err)
	}
	code, err := redisResultToInt64(result)
	if err != nil {
		return err
	}
	switch code {
	case 1:
		// Same worker ID refreshed; extend the lease deadline.
		w.setLeaseDeadline(time.Now())
		return nil
	case 0:
		// Another instance took our worker ID; we can no longer safely use it.
		// Fall back to a full registration to acquire a FRESH worker ID rather than
		// permanently bricking generation on this id.
		log.Warnf("worker ID %d was taken by another instance, acquiring a fresh worker ID", workerID)
		return w.reacquireFreshWorkerID(workerID)
	case -1:
		// Key expired (e.g. sustained Redis outage). The id may now be claimable by
		// anyone, so acquire a fresh worker ID via full registration.
		log.Warnf("worker ID %d key expired, acquiring a fresh worker ID", workerID)
		return w.reacquireFreshWorkerID(workerID)
	case -2:
		return fmt.Errorf("worker ID %d has invalid JSON format", workerID)
	default:
		return fmt.Errorf("re-register returned unknown status: %d", code)
	}
}

// reacquireFreshWorkerID clears the local registration state and performs a full
// RegisterWorkerID to obtain a brand-new worker ID. RegisterWorkerID updates the
// generator's worker ID (via onWorkerIDChange) BEFORE marking the worker healthy and
// restarting the heartbeat, so generation is never permanently bricked and never emits
// an ID with a stale/unowned worker id.
//
// IMPORTANT: the caller (heartbeatLoop -> tryReRegister) passes the heartbeat ctx, which
// this function is about to cancel. We MUST NOT derive the registration context from it,
// or registration would fail immediately with context.Canceled. We use a fresh,
// independent context off context.Background() with a bounded timeout instead.
func (w *WorkerIDManager) reacquireFreshWorkerID(previousWorkerID int64) error {
	// Reset state so RegisterWorkerID will run its acquisition loop. Mark unhealthy and
	// clear the lease deadline so IsHealthy blocks generation while recovery is in
	// progress. Stop the current heartbeat loop here; RegisterWorkerID starts a fresh
	// one on success. We must drop w.mu before calling heartbeatCancel's callback path
	// is unnecessary, but cancelling the context itself does not need a lock held by the
	// loop, so it is safe to cancel under w.mu (the loop only takes w.mu in its deferred
	// cleanup, which guards on heartbeatCtx == ctx and will be a no-op once we clear it).
	w.mu.Lock()
	atomic.StoreInt32(&w.healthy, 0)
	atomic.StoreInt64(&w.leaseDeadline, 0)
	w.workerID = -1
	w.registered = false
	if w.heartbeatCancel != nil {
		w.heartbeatCancel()
		w.heartbeatCancel = nil
		w.heartbeatCtx = nil
		w.heartbeatRunning = false
	}
	maxWorkerID := w.maxWorkerID
	w.mu.Unlock()

	if maxWorkerID < 0 {
		return fmt.Errorf("cannot reacquire worker ID: maxWorkerID unknown")
	}

	// First, a few synchronous attempts so a transient blip recovers immediately and the
	// heartbeat loop is back before we even return. RegisterWorkerID, on success: pushes
	// the fresh worker ID into the generator (onWorkerIDChange), starts a NEW heartbeat
	// loop on a valid background context, then marks the worker healthy.
	const syncAttempts = 3
	if newWorkerID, err := w.attemptRegisterFresh(maxWorkerID, syncAttempts); err == nil {
		log.Infof("acquired fresh worker ID %d (replacing %d)", newWorkerID, previousWorkerID)
		return nil
	}

	// Still failing (e.g. sustained Redis outage). Do NOT brick: spawn a detached
	// background loop that keeps retrying with backoff until success or shutdown. Health
	// stays 0 (generation blocked) until it succeeds. Only one loop may run at a time.
	if atomic.CompareAndSwapInt32(&w.recovering, 0, 1) {
		go w.backgroundRecover(maxWorkerID, previousWorkerID)
	}
	return fmt.Errorf("failed to reacquire fresh worker ID (was %d) after %d attempts; background recovery running", previousWorkerID, syncAttempts)
}

// attemptRegisterFresh runs up to `attempts` full RegisterWorkerID cycles, each on a
// fresh independent context (never derived from the cancelled heartbeat ctx), with
// backoff between attempts. Returns the new worker ID on success.
func (w *WorkerIDManager) attemptRegisterFresh(maxWorkerID int64, attempts int) (int64, error) {
	var lastErr error
	for i := 0; i < attempts; i++ {
		if atomic.LoadInt32(&w.shuttingDown) == 1 {
			return -1, fmt.Errorf("worker manager shutting down")
		}
		if i > 0 {
			// Backoff between attempts: 200ms, 400ms, ... capped at 5s.
			backoff := time.Duration(200*(1<<uint(i-1))) * time.Millisecond
			if backoff > 5*time.Second {
				backoff = 5 * time.Second
			}
			time.Sleep(backoff)
		}
		// Fresh, independent context — NOT a child of the just-cancelled heartbeat ctx.
		timeoutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		newWorkerID, err := w.RegisterWorkerID(timeoutCtx, maxWorkerID)
		cancel()
		if err == nil {
			return newWorkerID, nil
		}
		lastErr = err
		log.Warnf("fresh worker ID registration attempt %d/%d failed: %v", i+1, attempts, err)
	}
	return -1, lastErr
}

// backgroundRecover keeps retrying fresh-worker-ID acquisition with backoff until it
// succeeds or the manager is shutting down. Runs detached on context.Background(); health
// remains 0 (generation blocked) for the whole duration so no ID is emitted with an
// unowned worker id. Cleared via the recovering flag on exit.
func (w *WorkerIDManager) backgroundRecover(maxWorkerID, previousWorkerID int64) {
	defer atomic.StoreInt32(&w.recovering, 0)
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("eon-id background recovery recovered from panic: %v", r)
		}
	}()

	backoff := 1 * time.Second
	const maxBackoff = 10 * time.Second
	for {
		if atomic.LoadInt32(&w.shuttingDown) == 1 {
			log.Infof("eon-id background recovery stopping: worker manager shutting down")
			return
		}
		// If something else already re-registered (e.g. a new heartbeat recovered), stop.
		w.mu.RLock()
		alreadyRegistered := w.registered
		w.mu.RUnlock()
		if alreadyRegistered {
			return
		}

		timeoutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		newWorkerID, err := w.RegisterWorkerID(timeoutCtx, maxWorkerID)
		cancel()
		if err == nil {
			log.Infof("acquired fresh worker ID %d (replacing %d) via background recovery", newWorkerID, previousWorkerID)
			return
		}
		log.Warnf("eon-id background recovery attempt failed, retrying in %s: %v", backoff, err)
		time.Sleep(backoff)
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// sendHeartbeat sends heartbeat to maintain worker ID key TTL
// Uses Lua script to atomically verify instanceID and refresh TTL
func (w *WorkerIDManager) sendHeartbeat() error {
	if w.redisClient == nil {
		return fmt.Errorf("redis client is nil")
	}

	w.mu.RLock()
	workerID := w.workerID
	registerTime := w.registerTime
	instanceID := w.instanceID
	localIP := w.localIP
	serviceName := w.serviceName
	serviceVersion := w.serviceVersion
	w.mu.RUnlock()

	if workerID == -1 {
		return fmt.Errorf("worker ID not registered")
	}

	parent := w.heartbeatCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()

	key := w.getWorkerKey(workerID)

	workerInfo := WorkerInfo{
		WorkerID:       workerID,
		DatacenterID:   w.datacenterID,
		IP:             localIP,
		ServiceName:    serviceName,
		ServiceVersion: serviceVersion,
		RegisterTime:   registerTime.Unix(),
		LastHeartbeat:  time.Now().Unix(),
		InstanceID:     instanceID,
	}

	result, err := w.redisClient.Eval(ctx, LuaScriptHeartbeat, []string{key},
		workerInfo.String(), instanceID, int64(w.ttl.Seconds())).Result()
	if err != nil {
		return fmt.Errorf("heartbeat script execution failed: %w", err)
	}

	code, err := redisResultToInt64(result)
	if err != nil {
		return fmt.Errorf("heartbeat script result: %w", err)
	}
	switch code {
	case 1:
		// TTL was just refreshed to w.ttl from now; extend the lease deadline so
		// IsHealthy keeps allowing generation. The heartbeat interval (well below ttl)
		// guarantees this renews comfortably before the previous deadline elapses.
		w.setLeaseDeadline(time.Now())
		return nil // Success
	case 0:
		return fmt.Errorf("worker ID %d was taken by another instance", workerID)
	case -1:
		return fmt.Errorf("worker ID %d key has expired", workerID)
	case -2:
		return fmt.Errorf("worker ID %d has invalid JSON format", workerID)
	default:
		return fmt.Errorf("heartbeat returned unknown status: %d", code)
	}
}

// UnregisterWorkerID unregisters the worker ID.
// Only deletes the worker key and removes from registry when the key's instance_id matches this instance (safe for graceful shutdown).
func (w *WorkerIDManager) UnregisterWorkerID(ctx context.Context) error {
	// Signal any in-flight background recovery loop to stop resurrecting state.
	atomic.StoreInt32(&w.shuttingDown, 1)
	w.mu.Lock()
	if !w.registered {
		w.mu.Unlock()
		return nil // Not registered
	}
	if w.redisClient == nil {
		atomic.StoreInt32(&w.healthy, 0)
		if w.heartbeatCancel != nil {
			w.heartbeatCancel()
			w.heartbeatCancel = nil
			w.heartbeatCtx = nil
			w.heartbeatRunning = false
		}
		w.workerID = -1
		w.registered = false
		w.mu.Unlock()
		return fmt.Errorf("redis client is nil")
	}

	workerID := w.workerID
	instanceID := w.instanceID
	registryMember := fmt.Sprintf("%d:%d", w.datacenterID, w.workerID)

	// Mark as unhealthy and stop heartbeat first
	atomic.StoreInt32(&w.healthy, 0)
	promWorkerIDGauge.Set(-1)
	if w.heartbeatCancel != nil {
		w.heartbeatCancel()
		w.heartbeatCancel = nil
		w.heartbeatCtx = nil
		w.heartbeatRunning = false
	}
	w.workerID = -1
	w.registered = false
	w.mu.Unlock()

	key := w.getWorkerKey(workerID)
	registryKey := w.getRegistryKey()

	// Only delete if the key still belongs to this instance (instance_id match)
	infoStr, err := w.redisClient.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			// Key already expired or deleted; still remove from registry if present (idempotent)
			_ = w.redisClient.SRem(ctx, registryKey, registryMember).Err()
		}
		return nil
	}
	info, err := ParseWorkerInfo(infoStr)
	if err != nil {
		return nil
	}
	if info.InstanceID != instanceID {
		// Another instance took this worker ID; do not delete or SRem
		return nil
	}
	_ = w.redisClient.Del(ctx, key).Err()
	_ = w.redisClient.SRem(ctx, registryKey, registryMember).Err()
	return nil
}

// GetWorkerID returns the current worker ID
func (w *WorkerIDManager) GetWorkerID() int64 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.workerID
}

// GetRegisteredWorkers returns all registered workers.
// Stale registry members (worker key expired or missing, e.g. after power loss) are removed from the set (lazy cleanup).
func (w *WorkerIDManager) GetRegisteredWorkers(ctx context.Context) ([]WorkerInfo, error) {
	if w.redisClient == nil {
		return nil, fmt.Errorf("redis client is nil")
	}

	registryKey := w.getRegistryKey()

	members, err := w.redisClient.SMembers(ctx, registryKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get registry members: %w", err)
	}

	var workers []WorkerInfo
	for _, member := range members {
		parts := strings.Split(member, ":")
		if len(parts) != 2 {
			continue
		}

		datacenterID, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			continue
		}

		workerID, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue
		}

		// Get worker info; worker key has TTL and may have expired (e.g. instance died without Stop)
		key := w.getWorkerKeyForDatacenter(datacenterID, workerID)
		infoStr, err := w.redisClient.Get(ctx, key).Result()
		if err != nil {
			if err == redis.Nil {
				// Key expired or missing: remove stale member from registry (lazy cleanup)
				_ = w.redisClient.SRem(ctx, registryKey, member).Err()
			}
			continue
		}

		workerInfo, err := ParseWorkerInfo(infoStr)
		if err != nil {
			continue
		}

		workers = append(workers, *workerInfo)
	}

	return workers, nil
}

// NormalizeKeyPrefix ensures the key prefix ends with ":" or "_" for dc/worker/registry concatenation; aligns with DefaultRedisKeyPrefix.
func NormalizeKeyPrefix(prefix string) string {
	if prefix == "" {
		return DefaultRedisKeyPrefix
	}
	if last := prefix[len(prefix)-1]; last != ':' && last != '_' {
		return prefix + ":"
	}
	return prefix
}

// Helper methods (keyPrefix is normalized via NormalizeKeyPrefix at creation time)
func (w *WorkerIDManager) getWorkerKey(workerID int64) string {
	return fmt.Sprintf("%sdc:%d:worker:%d", w.keyPrefix, w.datacenterID, workerID)
}

func (w *WorkerIDManager) getWorkerKeyForDatacenter(datacenterID, workerID int64) string {
	return fmt.Sprintf("%sdc:%d:worker:%d", w.keyPrefix, datacenterID, workerID)
}

func (w *WorkerIDManager) getCounterKey() string {
	return fmt.Sprintf("%sdc:%d:counter", w.keyPrefix, w.datacenterID)
}

func (w *WorkerIDManager) getRegistryKey() string {
	return fmt.Sprintf("%sregistry", w.keyPrefix)
}

func (w *WorkerIDManager) generateInstanceID() string {
	// Include PID and random to reduce collision probability under high concurrency
	pid := os.Getpid()
	r := secureInt63n(100000)
	return fmt.Sprintf("instance-%d-%d-%d-%d-%d", time.Now().UnixNano(), w.datacenterID, pid, time.Now().UnixMicro()%10000, r)
}

func secureInt63n(max int64) int64 {
	if max <= 0 {
		return 0
	}
	n, err := crand.Int(crand.Reader, big.NewInt(max))
	if err != nil {
		return time.Now().UnixNano() % max
	}
	return n.Int64()
}

// WorkerInfo represents information about a registered worker
type WorkerInfo struct {
	WorkerID       int64  `json:"worker_id"`
	DatacenterID   int64  `json:"datacenter_id"`
	IP             string `json:"ip"`
	ServiceName    string `json:"service_name"`
	ServiceVersion string `json:"service_version"`
	RegisterTime   int64  `json:"register_time"`
	LastHeartbeat  int64  `json:"last_heartbeat"`
	InstanceID     string `json:"instance_id"`
}

// String returns JSON representation of WorkerInfo
func (wi *WorkerInfo) String() string {
	data, _ := json.Marshal(wi)
	return string(data)
}

// ParseWorkerInfo parses a WorkerInfo from JSON string
func ParseWorkerInfo(s string) (*WorkerInfo, error) {
	var info WorkerInfo
	if err := json.Unmarshal([]byte(s), &info); err != nil {
		return nil, fmt.Errorf("invalid worker info JSON: %w", err)
	}
	return &info, nil
}

// GetRegisterTime returns register time as time.Time
func (wi *WorkerInfo) GetRegisterTime() time.Time {
	return time.Unix(wi.RegisterTime, 0)
}

// GetLastHeartbeat returns last heartbeat as time.Time
func (wi *WorkerInfo) GetLastHeartbeat() time.Time {
	return time.Unix(wi.LastHeartbeat, 0)
}

// WorkerManagerConfig holds configuration for the worker manager
type WorkerManagerConfig struct {
	KeyPrefix         string
	TTL               time.Duration
	HeartbeatInterval time.Duration
	ServiceName       string // Application name (e.g. from lynx.GetName())
	ServiceVersion    string // Application version (e.g. from lynx.GetVersion())
}

// DefaultWorkerManagerConfig returns default worker manager configuration
func DefaultWorkerManagerConfig() *WorkerManagerConfig {
	return &WorkerManagerConfig{
		KeyPrefix:         DefaultRedisKeyPrefix,
		TTL:               DefaultWorkerIDTTL,
		HeartbeatInterval: DefaultHeartbeatInterval,
	}
}
