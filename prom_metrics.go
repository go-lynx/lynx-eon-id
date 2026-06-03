package eonId

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Prometheus metrics for the eon-id plugin.
// All metrics are under the "lynx_eon_id" namespace and registered with the
// default Prometheus registry via promauto (registered once at program start).
var (
	// promIDsGeneratedTotal counts the total number of IDs successfully generated.
	promIDsGeneratedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "lynx",
		Subsystem: "eon_id",
		Name:      "ids_generated_total",
		Help:      "Total number of Snowflake IDs successfully generated.",
	})

	// promGenerationErrorsTotal counts the total number of ID generation failures.
	promGenerationErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "lynx",
		Subsystem: "eon_id",
		Name:      "generation_errors_total",
		Help:      "Total number of ID generation failures.",
	})

	// promClockDriftEventsTotal counts the number of clock backward events detected.
	promClockDriftEventsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "lynx",
		Subsystem: "eon_id",
		Name:      "clock_drift_events_total",
		Help:      "Total number of clock backward (drift) events detected during ID generation.",
	})

	// promSequenceOverflowsTotal counts the number of sequence overflow events
	// (sequence exhausted within a single millisecond, causing a wait).
	promSequenceOverflowsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "lynx",
		Subsystem: "eon_id",
		Name:      "sequence_overflows_total",
		Help:      "Total number of sequence overflow events (sequence space exhausted within a millisecond).",
	})

	// promWorkerIDFailuresTotal counts the number of worker ID acquisition failures.
	promWorkerIDFailuresTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "lynx",
		Subsystem: "eon_id",
		Name:      "worker_id_failures_total",
		Help:      "Total number of worker ID registration or heartbeat failures.",
	})

	// promWorkerIDHeartbeatFailuresTotal counts transient heartbeat failures.
	promWorkerIDHeartbeatFailuresTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "lynx",
		Subsystem: "eon_id",
		Name:      "worker_id_heartbeat_failures_total",
		Help:      "Total number of worker ID heartbeat failures (each missed beat increments by 1).",
	})

	// promGenerationLatency observes ID generation latency in seconds.
	promGenerationLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: "lynx",
		Subsystem: "eon_id",
		Name:      "generation_latency_seconds",
		Help:      "Latency of individual ID generation calls in seconds.",
		Buckets:   []float64{0.000001, 0.00001, 0.0001, 0.001, 0.005, 0.01, 0.05, 0.1},
	})

	// promWorkerIDGauge tracks the current registered worker ID (-1 if not registered).
	promWorkerIDGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "lynx",
		Subsystem: "eon_id",
		Name:      "worker_id",
		Help:      "Current registered worker ID (-1 if not yet registered).",
	})
)
