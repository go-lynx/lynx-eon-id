# Lynx Snowflake ID Generator Plugin

一个高性能、分布式唯一 ID 生成器插件，基于 Twitter Snowflake 算法实现，专为 [Go-Lynx](https://github.com/go-lynx/lynx) 微服务框架设计。

## ✨ 特性

- 🚀 **高性能**: 单节点每毫秒可生成数千个唯一 ID
- 🔄 **分布式**: 支持多数据中心、多节点部署，保证全局唯一
- ⏰ **时钟漂移保护**: 内置时钟回拨检测与处理机制
- 📝 **自动注册**: 基于 Redis 的 Worker ID 自动注册与心跳维护
- 📊 **指标监控**: 内置详细的性能指标收集
- 🔒 **线程安全**: 完全并发安全的 ID 生成
- ⚡ **序列缓存**: 可选的序列号缓存优化

## 📦 安装

```bash
go get github.com/go-lynx/lynx/plugins/snowflake
```

## 🚀 快速开始

### 1. 配置文件

在 `config.yml` 中添加插件配置：

```yaml
lynx:
  snowflake:
    datacenter_id: 1
    auto_register_worker_id: true
    redis_key_prefix: "lynx:snowflake:worker"
    worker_id_ttl: "30s"
    heartbeat_interval: "10s"
    enable_clock_drift_protection: true
    enable_sequence_cache: true
    sequence_cache_size: 1000
    enable_metrics: true
    redis_plugin_name: "default"
```

### 2. 使用插件

```go
package main

import (
    "fmt"
    
    snowflake "github.com/go-lynx/lynx/plugins/snowflake"
)

func main() {
    // 获取 Snowflake 生成器实例
    generator := snowflake.GetSnowflakeGenerator()
    if generator == nil {
        panic("snowflake generator not initialized")
    }
    
    // 生成唯一 ID
    id, err := generator.GenerateID()
    if err != nil {
        panic(err)
    }
    fmt.Printf("Generated ID: %d\n", id)
    
    // 生成带元数据的 ID
    id, metadata, err := generator.GenerateIDWithMetadata()
    if err != nil {
        panic(err)
    }
    fmt.Printf("ID: %d, Timestamp: %v, WorkerID: %d\n", 
        id, metadata.Timestamp, metadata.WorkerID)
    
    // 解析已有的 ID
    parsed, err := generator.ParseID(id)
    if err != nil {
        panic(err)
    }
    fmt.Printf("Parsed - Timestamp: %v, DatacenterID: %d, WorkerID: %d, Sequence: %d\n",
        parsed.Timestamp, parsed.DatacenterID, parsed.WorkerID, parsed.Sequence)
}
```

## ⚙️ 配置说明

### 基础配置

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `datacenter_id` | int | 1 | 数据中心 ID (0-31) |
| `worker_id` | int | 0 | Worker ID，若不设置则自动注册 |
| `auto_register_worker_id` | bool | true | 启用基于 Redis 的自动 Worker ID 注册 |
| `redis_key_prefix` | string | "snowflake:" | Redis 键前缀 |
| `worker_id_ttl` | duration | 30s | Worker ID 注册 TTL |
| `heartbeat_interval` | duration | 10s | 心跳间隔 |

### 时钟漂移保护

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `enable_clock_drift_protection` | bool | true | 启用时钟漂移保护 |
| `max_clock_drift` | duration | 5s | 最大允许的时钟回拨 |
| `clock_check_interval` | duration | 1s | 时钟检查间隔 |
| `clock_drift_action` | string | "wait" | 时钟漂移处理策略: `wait`/`error`/`ignore` |

### 性能配置

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `enable_sequence_cache` | bool | false | 启用序列号缓存 |
| `sequence_cache_size` | int | 1000 | 序列缓存大小 |
| `enable_metrics` | bool | true | 启用指标收集 |

### 高级配置

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `custom_epoch` | int64 | 1609459200000 | 自定义纪元时间戳 (毫秒) |
| `worker_id_bits` | int | 5 | Worker ID 位数 (1-20) |
| `sequence_bits` | int | 12 | 序列号位数 (1-20) |
| `redis_plugin_name` | string | "redis" | Redis 插件名称 |
| `redis_db` | int | 0 | Redis 数据库编号 |

## 🏗️ ID 结构

默认的 64 位 ID 结构：

```
+--------------------------------------------------------------------------+
| 1 bit  |        41 bits        | 5 bits  |  5 bits  |     12 bits       |
| unused |      timestamp        | dc_id   | worker   |     sequence      |
+--------------------------------------------------------------------------+
```

- **1 bit**: 符号位（始终为 0）
- **41 bits**: 时间戳（毫秒级，可用约 69 年）
- **5 bits**: 数据中心 ID（0-31）
- **5 bits**: Worker ID（0-31）
- **12 bits**: 序列号（每毫秒 0-4095）

## 🔧 环境配置示例

### 生产环境

```yaml
lynx:
  snowflake:
    datacenter_id: 1
    auto_register_worker_id: true
    redis_key_prefix: "prod:lynx:snowflake:worker"
    worker_id_ttl: "60s"
    heartbeat_interval: "20s"
    enable_clock_drift_protection: true
    max_clock_drift: "1s"
    clock_drift_action: "error"
    enable_sequence_cache: true
    sequence_cache_size: 5000
    enable_metrics: true
```

### 开发环境

```yaml
lynx:
  snowflake:
    datacenter_id: 0
    worker_id: 1
    auto_register_worker_id: false
    enable_clock_drift_protection: false
    enable_sequence_cache: false
    enable_metrics: false
```

### 高并发场景

```yaml
lynx:
  snowflake:
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

## 🏢 多数据中心部署

在多数据中心部署时，每个数据中心使用不同的 `datacenter_id`：

- 数据中心 A: `datacenter_id: 0`
- 数据中心 B: `datacenter_id: 1`
- 数据中心 C: `datacenter_id: 2`

这确保了不同数据中心生成的 ID 不会冲突。

## 📊 健康检查

插件提供详细的健康检查报告：

```go
generator := snowflake.GetSnowflakeGenerator()
health := generator.GetHealth()

fmt.Printf("Status: %s\n", health.Status)
fmt.Printf("Message: %s\n", health.Message)
fmt.Printf("Details: %+v\n", health.Details)
```

健康状态：
- `healthy`: 正常运行
- `degraded`: 存在警告（如时钟回拨事件、高错误率）
- `unhealthy`: 服务不可用

## 🧪 运行测试

```bash
# 运行所有测试
go test ./...

# 运行性能测试
go test -bench=. -benchmem

# 运行压力测试
go test -run TestStress
```

## 📄 许可证

MIT License

## 🔗 相关链接

- [Go-Lynx 框架](https://github.com/go-lynx/lynx)
- [Twitter Snowflake](https://github.com/twitter-archive/snowflake)

