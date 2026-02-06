# Lynx Eon-ID Plugin

A high-performance, distributed unique ID generator plugin based on the Twitter Snowflake algorithm, designed for the [Go-Lynx](https://github.com/go-lynx/lynx) microservices framework.

## ✨ Features

- 🚀 **High Performance**: Generate thousands of unique IDs per millisecond on a single node
- 🔄 **Distributed**: Support multi-datacenter, multi-node deployment with globally unique IDs
- ⏰ **Clock Drift Protection**: Built-in clock backward detection and handling mechanism
- 📝 **Auto Registration**: Redis-based Worker ID auto-registration with heartbeat maintenance
- 📊 **Metrics Monitoring**: Built-in detailed performance metrics collection
- 🔒 **Thread Safe**: Fully concurrent-safe ID generation
- ⚡ **Sequence Cache**: Optional sequence number caching optimization

## 📦 Installation

```bash
go get github.com/go-lynx/lynx-eon-id
```

## 🚀 Quick Start

### 1. Configuration

Add plugin configuration in `config.yml`:

```yaml
lynx:
  eon-id:
    datacenter_id: 1
    auto_register_worker_id: true
    redis_key_prefix: "lynx:eon-id:"
    worker_id_ttl: "30s"
    heartbeat_interval: "10s"
    enable_clock_drift_protection: true
    enable_sequence_cache: true
    sequence_cache_size: 1000
    enable_metrics: true
    redis_plugin_name: "redis"
```

### 2. Usage

推荐使用包级方法（需在 Lynx 启动并完成插件初始化后调用）：

```go
package main

import (
    "fmt"
    
    eonid "github.com/go-lynx/lynx-eon-id"
)

func main() {
    // 方式一：包级生成（推荐）
    id, err := eonid.GenerateID()
    if err != nil {
        panic(err)
    }
    fmt.Printf("Generated ID: %d\n", id)
    
    // 方式二：获取插件实例后调用
    plugin, err := eonid.GetSnowflakePlugin()
    if err != nil {
        panic(err)
    }
    id, metadata, err := plugin.GenerateIDWithMetadata()
    if err != nil {
        panic(err)
    }
    fmt.Printf("ID: %d, Timestamp: %v, WorkerID: %d\n", id, metadata.Timestamp, metadata.WorkerID)
    
    parsed, err := plugin.ParseID(id)
    if err != nil {
        panic(err)
    }
    fmt.Printf("Parsed - Timestamp: %v, DatacenterID: %d, WorkerID: %d, Sequence: %d\n",
        parsed.Timestamp, parsed.DatacenterID, parsed.WorkerID, parsed.Sequence)
}
```

## ⚙️ Configuration Reference

### Basic Configuration

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `datacenter_id` | int | 1 | Datacenter ID (0-31) |
| `worker_id` | int | 0 | Worker ID, auto-registered if not set |
| `auto_register_worker_id` | bool | true | Enable Redis-based auto Worker ID registration |
| `redis_key_prefix` | string | "lynx:eon-id:" | Redis key prefix（建议以 ":" 结尾，未结尾时自动补全） |
| `worker_id_ttl` | duration | 30s | Worker ID registration TTL |
| `heartbeat_interval` | duration | 10s | Heartbeat interval |

### Clock Drift Protection

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `enable_clock_drift_protection` | bool | true | Enable clock drift protection |
| `max_clock_drift` | duration | 5s | Maximum allowed clock backward |
| `clock_check_interval` | duration | 1s | Clock check interval |
| `clock_drift_action` | string | "wait" | Clock drift handling strategy: `wait`/`error`/`ignore` |

### Performance Configuration

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `enable_sequence_cache` | bool | false | Enable sequence number caching |
| `sequence_cache_size` | int | 1000 | Sequence cache size |
| `enable_metrics` | bool | true | Enable metrics collection |

### Advanced Configuration

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `custom_epoch` | int64 | 1609459200000 | Custom epoch timestamp (milliseconds) |
| `worker_id_bits` | int | 5 | Worker ID bits (1-20) |
| `sequence_bits` | int | 12 | Sequence bits (1-20) |
| `redis_plugin_name` | string | "redis" | Redis 插件名（需与框架注册名一致） |
| `redis_db` | int | 0 | Redis database number |

## 🏗️ ID Structure

Default 64-bit ID structure:

```
+--------------------------------------------------------------------------+
| 1 bit  |        41 bits        | 5 bits  |  5 bits  |     12 bits       |
| unused |      timestamp        | dc_id   | worker   |     sequence      |
+--------------------------------------------------------------------------+
```

- **1 bit**: Sign bit (always 0)
- **41 bits**: Timestamp (milliseconds, ~69 years lifespan)
- **5 bits**: Datacenter ID (0-31)
- **5 bits**: Worker ID (0-31)
- **12 bits**: Sequence number (0-4095 per millisecond)

## 🔧 Environment Configuration Examples

### Production

```yaml
lynx:
  eon-id:
    datacenter_id: 1
    auto_register_worker_id: true
    redis_key_prefix: "prod:lynx:eon-id:"
    worker_id_ttl: "60s"
    heartbeat_interval: "20s"
    enable_clock_drift_protection: true
    max_clock_drift: "1s"
    clock_drift_action: "error"
    enable_sequence_cache: true
    sequence_cache_size: 5000
    enable_metrics: true
```

### Development

```yaml
lynx:
  eon-id:
    datacenter_id: 0
    worker_id: 1
    auto_register_worker_id: false
    enable_clock_drift_protection: false
    enable_sequence_cache: false
    enable_metrics: false
```

### High Concurrency

```yaml
lynx:
  eon-id:
    datacenter_id: 2
    auto_register_worker_id: true
    worker_id_ttl: "120s"
    heartbeat_interval: "30s"
    enable_clock_drift_protection: true
    max_clock_drift: "10s"
    clock_drift_action: "ignore"
    enable_sequence_cache: true
    sequence_cache_size: 10000
    worker_id_bits: 8
    sequence_bits: 14
```

## 🏢 Multi-Datacenter Deployment

When deploying across multiple datacenters, use different `datacenter_id` for each:

- Datacenter A: `datacenter_id: 0`
- Datacenter B: `datacenter_id: 1`
- Datacenter C: `datacenter_id: 2`

This ensures IDs generated from different datacenters will never conflict.

## 📊 Health Check

The plugin provides detailed health check reports:

```go
plugin, err := eonid.GetSnowflakePlugin()
if err != nil {
    panic(err)
}
health := plugin.GetHealth()

fmt.Printf("Status: %s\n", health.Status)
fmt.Printf("Message: %s\n", health.Message)
fmt.Printf("Details: %+v\n", health.Details)
```

Health statuses:
- `healthy`: Operating normally
- `degraded`: Warnings present (e.g., clock backward events, high error rate)
- `unhealthy`: Service unavailable

## 🧪 Running Tests

```bash
# Run all tests
go test ./...

# Run benchmark tests
go test -bench=. -benchmem

# Run stress tests
go test -run TestStress
```

## 📄 License

MIT License

## 🔗 Related Links

- [Go-Lynx Framework](https://github.com/go-lynx/lynx)
- [Twitter Snowflake](https://github.com/twitter-archive/snowflake)
