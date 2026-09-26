// Package prometheusmetrics exposes rep operational metrics through a
// dedicated Prometheus registry.  The registry is updated by the
// IngressClient wrapper that sits between the rep and its Loggregator
// ingress client; no second executor polling loop is introduced.
//
// On a failed executor sample the capacity/usage gauges are removed from
// the exposition and the error counter is incremented.  Stale values are
// never presented as current.
package prometheusmetrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

const namespace = "cf_rep"

// RepMetrics holds the Prometheus metrics published by the rep.
// It implements prometheus.Collector so that capacity/usage gauges
// can be atomically replaced on each reporter tick.
type RepMetrics struct {
	mu sync.Mutex

	// observation is the latest capacity/usage snapshot.  A nil pointer
	// means the last tick failed and gauges must not be exposed.
	observation *Observation

	// descs is the fixed set of metric descriptors registered with the
	// registry.  Prometheus requires Describe() to return the same set
	// on every call.
	descs []*prometheus.Desc

	// ---- self-registered (push-style) metrics ----

	// BulkSyncDuration is a histogram of BBS bulk reconciliation durations.
	BulkSyncDuration prometheus.Histogram

	// StrandedEvacuatingActualLRPs is set directly from evacuation cleanup.
	StrandedEvacuatingActualLRPs prometheus.Gauge

	// SampleErrors counts failed executor samples (bounded: increments only
	// on a failed sample, resets on process restart).
	SampleErrors prometheus.Counter

	// registry is the dedicated Prometheus registry (not the global default).
	registry *prometheus.Registry
}

// Observation is a point-in-time snapshot of the executor reporter values.
// All *_bytes fields are already in bytes (MiB conversion is done by the
// IngressClient wrapper before calling SetObservation).
type Observation struct {
	CapacityTotalMemoryBytes     float64
	CapacityRemainingMemoryBytes float64
	CapacityAllocatedMemoryBytes float64
	CapacityTotalDiskBytes       float64
	CapacityRemainingDiskBytes   float64
	CapacityAllocatedDiskBytes   float64
	CapacityTotalContainers      float64
	CapacityRemainingContainers  float64
	ContainerUsageMemoryBytes    float64
	ContainerUsageDiskBytes      float64
	ContainersAll                float64
	ContainersStarting           float64
}

// Descriptors for the capacity/usage gauges.
var (
	descCapacityTotalMemoryBytes     = prometheus.NewDesc(namespace+"_capacity_total_memory_bytes", "Total executor memory in bytes.", nil, nil)
	descCapacityRemainingMemoryBytes = prometheus.NewDesc(namespace+"_capacity_remaining_memory_bytes", "Remaining executor memory in bytes.", nil, nil)
	descCapacityAllocatedMemoryBytes = prometheus.NewDesc(namespace+"_capacity_allocated_memory_bytes", "Allocated executor memory in bytes (total minus remaining).", nil, nil)
	descCapacityTotalDiskBytes       = prometheus.NewDesc(namespace+"_capacity_total_disk_bytes", "Total executor disk in bytes.", nil, nil)
	descCapacityRemainingDiskBytes   = prometheus.NewDesc(namespace+"_capacity_remaining_disk_bytes", "Remaining executor disk in bytes.", nil, nil)
	descCapacityAllocatedDiskBytes   = prometheus.NewDesc(namespace+"_capacity_allocated_disk_bytes", "Allocated executor disk in bytes (total minus remaining).", nil, nil)
	descCapacityTotalContainers      = prometheus.NewDesc(namespace+"_capacity_total_containers", "Configured container capacity.", nil, nil)
	descCapacityRemainingContainers  = prometheus.NewDesc(namespace+"_capacity_remaining_containers", "Remaining container slots.", nil, nil)
	descContainerUsageMemoryBytes    = prometheus.NewDesc(namespace+"_container_usage_memory_bytes", "Aggregate memory usage across containers on this rep.", nil, nil)
	descContainerUsageDiskBytes      = prometheus.NewDesc(namespace+"_container_usage_disk_bytes", "Aggregate disk usage across containers on this rep.", nil, nil)
	descContainers                   = prometheus.NewDesc(namespace+"_containers", "Container count by state.", []string{"state"}, nil)
)

// NewRepMetrics creates a dedicated Prometheus registry with the rep
// metric families.  The returned RepMetrics must be registered via its
// Registry() before the HTTP server is started.
func NewRepMetrics() *RepMetrics {
	m := &RepMetrics{
		descs: []*prometheus.Desc{
			descCapacityTotalMemoryBytes,
			descCapacityRemainingMemoryBytes,
			descCapacityAllocatedMemoryBytes,
			descCapacityTotalDiskBytes,
			descCapacityRemainingDiskBytes,
			descCapacityAllocatedDiskBytes,
			descCapacityTotalContainers,
			descCapacityRemainingContainers,
			descContainerUsageMemoryBytes,
			descContainerUsageDiskBytes,
			descContainers,
		},
		BulkSyncDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "bulk_sync_duration_seconds",
			Help:      "BBS bulk reconciliation duration in seconds.",
			Buckets:   prometheus.DefBuckets,
		}),
		StrandedEvacuatingActualLRPs: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "stranded_evacuating_actual_lrps",
			Help:      "Last evacuation-cleanup observation of stranded evacuating actual LRPs.",
		}),
		SampleErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "sample_errors_total",
			Help:      "Total number of failed executor samples.",
		}),
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(m) // capacity/usage gauges via the Collector interface
	reg.MustRegister(m.BulkSyncDuration)
	reg.MustRegister(m.StrandedEvacuatingActualLRPs)
	reg.MustRegister(m.SampleErrors)
	m.registry = reg
	return m
}

// Registry returns the dedicated Prometheus registry for use with
// promhttp.HandlerFor.
func (m *RepMetrics) Registry() *prometheus.Registry {
	return m.registry
}

// SetObservation atomically replaces the current capacity/usage snapshot.
func (m *RepMetrics) SetObservation(obs *Observation) {
	m.mu.Lock()
	m.observation = obs
	m.mu.Unlock()
}

// ClearObservation marks the current observation as stale (e.g. after an
// executor sample failure).  The next scrape will not expose capacity/usage
// gauges.
func (m *RepMetrics) ClearObservation() {
	m.mu.Lock()
	m.observation = nil
	m.mu.Unlock()
}

// Describe implements prometheus.Collector.
func (m *RepMetrics) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range m.descs {
		ch <- d
	}
}

// Collect implements prometheus.Collector.  If the last executor sample
// failed (observation is nil), no capacity/usage metrics are returned.
func (m *RepMetrics) Collect(ch chan<- prometheus.Metric) {
	m.mu.Lock()
	obs := m.observation
	m.mu.Unlock()

	if obs == nil {
		return
	}

	ch <- prometheus.MustNewConstMetric(descCapacityTotalMemoryBytes, prometheus.GaugeValue, obs.CapacityTotalMemoryBytes)
	ch <- prometheus.MustNewConstMetric(descCapacityRemainingMemoryBytes, prometheus.GaugeValue, obs.CapacityRemainingMemoryBytes)
	ch <- prometheus.MustNewConstMetric(descCapacityAllocatedMemoryBytes, prometheus.GaugeValue, obs.CapacityAllocatedMemoryBytes)
	ch <- prometheus.MustNewConstMetric(descCapacityTotalDiskBytes, prometheus.GaugeValue, obs.CapacityTotalDiskBytes)
	ch <- prometheus.MustNewConstMetric(descCapacityRemainingDiskBytes, prometheus.GaugeValue, obs.CapacityRemainingDiskBytes)
	ch <- prometheus.MustNewConstMetric(descCapacityAllocatedDiskBytes, prometheus.GaugeValue, obs.CapacityAllocatedDiskBytes)
	ch <- prometheus.MustNewConstMetric(descCapacityTotalContainers, prometheus.GaugeValue, obs.CapacityTotalContainers)
	ch <- prometheus.MustNewConstMetric(descCapacityRemainingContainers, prometheus.GaugeValue, obs.CapacityRemainingContainers)
	ch <- prometheus.MustNewConstMetric(descContainerUsageMemoryBytes, prometheus.GaugeValue, obs.ContainerUsageMemoryBytes)
	ch <- prometheus.MustNewConstMetric(descContainerUsageDiskBytes, prometheus.GaugeValue, obs.ContainerUsageDiskBytes)
	ch <- prometheus.MustNewConstMetric(descContainers, prometheus.GaugeValue, obs.ContainersAll, "all")
	ch <- prometheus.MustNewConstMetric(descContainers, prometheus.GaugeValue, obs.ContainersStarting, "starting")
}
