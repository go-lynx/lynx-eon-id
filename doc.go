// Package eonId provides a distributed Snowflake-style unique ID generator plugin
// for the Lynx framework.
//
// The plugin generates 64-bit IDs composed of:
//   - 41-bit timestamp (milliseconds since custom epoch, ~69 year lifespan)
//   - 5-bit datacenter ID (0–31)
//   - 5-bit worker ID (0–31, or up to 20 bits configurable)
//   - 12-bit sequence number (0–4095 per millisecond, configurable)
//
// Clock drift protection prevents duplicate IDs when the system clock moves
// backward, using one of three configurable actions: wait, error, or ignore.
// Worker IDs are allocated and refreshed via Redis with TTL-based heartbeats,
// guaranteeing uniqueness across service instances. When Redis is unavailable
// the worker ID may be configured statically as a fallback.
//
// Prometheus metrics are exposed under the "lynx_eon_id" namespace and include
// ID generation rate, clock drift event count, sequence overflow count, and
// worker ID acquisition failure count.
//
// Usage:
//
//	// Register the plugin with the Lynx app (auto-registered via init())
//	import _ "github.com/go-lynx/lynx-eon-id"
//
//	// Generate an ID
//	id, err := eonId.GenerateID()
package eonId
