package eonId

import (
	"context"
	"fmt"
	"time"

	lynxlog "github.com/go-lynx/lynx/log"
	"github.com/go-lynx/lynx/plugins"
)

func (p *PlugSnowflake) PluginProtocol() plugins.PluginProtocol {
	protocol := p.BasePlugin.PluginProtocol()
	protocol.ContextLifecycle = true
	return protocol
}

func (p *PlugSnowflake) IsContextAware() bool {
	return true
}

func (p *PlugSnowflake) InitializeContext(ctx context.Context, plugin plugins.Plugin, rt plugins.Runtime) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("initialize canceled before start: %w", err)
	}
	return p.BasePlugin.Initialize(plugin, rt)
}

func (p *PlugSnowflake) StartContext(ctx context.Context, plugin plugins.Plugin) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("start canceled before execution: %w", err)
	}
	if p.Status(plugin) == plugins.StatusActive {
		return plugins.ErrPluginAlreadyActive
	}

	p.SetStatus(plugins.StatusInitializing)
	p.EmitEvent(plugins.PluginEvent{
		Type:     plugins.EventPluginStarting,
		Priority: plugins.PriorityNormal,
		Source:   "StartContext",
		Category: "lifecycle",
	})

	if err := p.startupTasksContext(ctx); err != nil {
		p.SetStatus(plugins.StatusFailed)
		return plugins.NewPluginError(p.ID(), "Start", "Failed to perform startup tasks", err)
	}

	if err := p.CheckHealth(); err != nil {
		p.SetStatus(plugins.StatusFailed)
		return fmt.Errorf("plugin %s health check failed: %w", plugin.Name(), err)
	}

	p.SetStatus(plugins.StatusActive)
	p.EmitEvent(plugins.PluginEvent{
		Type:     plugins.EventPluginStarted,
		Priority: plugins.PriorityNormal,
		Source:   "StartContext",
		Category: "lifecycle",
	})

	return nil
}

func (p *PlugSnowflake) StopContext(ctx context.Context, plugin plugins.Plugin) error {
	return p.stopWithContext(ctx, plugin, "StopContext")
}

func (p *PlugSnowflake) stopWithContext(ctx context.Context, plugin plugins.Plugin, source string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("stop canceled before execution: %w", err)
	}
	if p.Status(plugin) != plugins.StatusActive {
		return plugins.NewPluginError(p.ID(), "Stop", "Plugin must be active to stop", plugins.ErrPluginNotActive)
	}

	p.SetStatus(plugins.StatusStopping)
	p.EmitEvent(plugins.PluginEvent{
		Type:     plugins.EventPluginStopping,
		Priority: plugins.PriorityNormal,
		Source:   source,
		Category: "lifecycle",
	})

	if err := p.cleanupTasksContext(ctx); err != nil {
		p.SetStatus(plugins.StatusFailed)
		return plugins.NewPluginError(p.ID(), "Stop", "Failed to perform cleanup tasks", err)
	}

	p.SetStatus(plugins.StatusTerminated)
	p.EmitEvent(plugins.PluginEvent{
		Type:     plugins.EventPluginStopped,
		Priority: plugins.PriorityNormal,
		Source:   source,
		Category: "lifecycle",
	})

	return nil
}

func (p *PlugSnowflake) startupTasksContext(parentCtx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.conf == nil || !p.conf.AutoRegisterWorkerId || p.workerManager == nil || p.redisClient == nil {
		return nil
	}

	ctx, cancel := p.createTimeoutContext(parentCtx, 10*time.Second)
	defer cancel()

	if p.conf.WorkerId > 0 {
		if err := p.workerManager.RegisterSpecificWorkerID(ctx, int64(p.conf.WorkerId)); err == nil {
			lynxlog.Infof("registered specific worker ID: %d", p.conf.WorkerId)
			return nil
		} else {
			lynxlog.Warnf("failed to register specific worker ID %d: %v, trying auto-register", p.conf.WorkerId, err)
		}
	}

	maxWorkerID := int64((1 << p.conf.WorkerIdBits) - 1)
	if maxWorkerID == 0 {
		maxWorkerID = 31
	}
	workerID, err := p.workerManager.RegisterWorkerID(ctx, maxWorkerID)
	if err != nil {
		return fmt.Errorf("failed to auto-register worker ID: %w", err)
	}
	if p.generator != nil {
		p.generator.mu.Lock()
		p.generator.workerID = workerID
		p.generator.mu.Unlock()
	}
	lynxlog.Infof("auto-registered worker ID: %d", workerID)
	return nil
}

func (p *PlugSnowflake) cleanupTasksContext(parentCtx context.Context) error {
	p.shutdownOnce.Do(func() { close(p.shutdownCh) })

	var cleanupErr error
	p.stopCleanupOnce.Do(func() {
		cleanupErr = p.doStopCleanupContext(parentCtx)
	})
	return cleanupErr
}

func (p *PlugSnowflake) doStopCleanupContext(parentCtx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var firstErr error
	if p.workerManager != nil {
		ctx, cancel := p.createTimeoutContext(parentCtx, 5*time.Second)
		defer cancel()
		if err := p.workerManager.UnregisterWorkerID(ctx); err != nil {
			lynxlog.Warnf("failed to unregister worker ID: %v", err)
			firstErr = err
		} else {
			lynxlog.Infof("unregistered worker ID")
		}
	}
	if p.generator != nil {
		ctx, cancel := p.createTimeoutContext(parentCtx, 5*time.Second)
		defer cancel()
		if err := p.generator.Shutdown(ctx); err != nil && firstErr == nil {
			lynxlog.Warnf("failed to shutdown generator: %v", err)
			firstErr = err
		}
	}
	return firstErr
}

func (p *PlugSnowflake) createTimeoutContext(parentCtx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if deadline, ok := parentCtx.Deadline(); ok && time.Until(deadline) < timeout {
		return parentCtx, func() {}
	}
	return context.WithTimeout(parentCtx, timeout)
}
