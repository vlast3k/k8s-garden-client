package prometheusmetrics

import (
	"time"

	loggingclient "code.cloudfoundry.org/diego-logging-client"
	loggregator "code.cloudfoundry.org/go-loggregator/v9"
)

// mebibytesToBytes converts MiB (as reported by the executor) to bytes.
const mebibytesToBytes = 1024 * 1024

// IngressClientWrapper wraps a loggingclient.IngressClient and mirrors
// rep-level metric emissions to a RepMetrics Prometheus registry.
//
// Only the rep-level operational metrics are intercepted:
//   - SendMebiBytes:  capacity and aggregate container usage
//   - SendMetric:     container counts, stranded evacuating LRPs
//   - SendDuration:   bulk sync duration
//
// All other calls (app logs, container envelopes, counters, etc.) are
// forwarded unchanged to the inner client.
type IngressClientWrapper struct {
	inner   loggingclient.IngressClient
	metrics *RepMetrics

	// pending accumulates partial capacity/usage observations within one
	// reporter tick.  When the reporter emits all expected metrics in
	// sequence, the wrapper publishes the complete observation atomically.
	// A failed reporter tick leaves pending incomplete and the wrapper
	// calls ClearObservation.
	pending *Observation

	// seen tracks which capacity/usage metrics have been received in
	// the current tick.  When all expected metrics arrive, the observation
	// is published.
	seen uint16
}

// expectedMask is the bitmask value when all 12 capacity/usage metrics
// have been received (bits 0..11).
const expectedMask uint16 = (1 << 12) - 1

// Bit positions for each capacity/usage metric.
const (
	bitCapTotalMem uint16 = 1 << iota
	bitCapRemMem
	bitCapAllocMem
	bitCapTotalDisk
	bitCapRemDisk
	bitCapAllocDisk
	bitContainerUsageMem
	bitContainerUsageDisk
	bitCapTotalContainers
	bitCapRemContainers
	bitContainerCountAll
	bitContainerCountStarting
)

// metricNameToBit maps Loggregator metric names to their bitmask position.
var metricNameToBit = map[string]uint16{
	"CapacityTotalMemory":        bitCapTotalMem,
	"CapacityRemainingMemory":    bitCapRemMem,
	"CapacityAllocatedMemory":    bitCapAllocMem,
	"CapacityTotalDisk":          bitCapTotalDisk,
	"CapacityRemainingDisk":      bitCapRemDisk,
	"CapacityAllocatedDisk":      bitCapAllocDisk,
	"ContainerUsageMemory":       bitContainerUsageMem,
	"ContainerUsageDisk":         bitContainerUsageDisk,
	"CapacityTotalContainers":    bitCapTotalContainers,
	"CapacityRemainingContainers": bitCapRemContainers,
	"ContainerCount":             bitContainerCountAll,
	"StartingContainerCount":     bitContainerCountStarting,
}

// NewIngressClientWrapper creates a wrapper around the given IngressClient
// that also updates the given RepMetrics.
func NewIngressClientWrapper(inner loggingclient.IngressClient, metrics *RepMetrics) *IngressClientWrapper {
	return &IngressClientWrapper{
		inner:   inner,
		metrics: metrics,
		pending: &Observation{},
	}
}

// ---- Intercepted methods ----

func (w *IngressClientWrapper) SendMebiBytes(name string, value int, opts ...loggregator.EmitGaugeOption) error {
	err := w.inner.SendMebiBytes(name, value, opts...)
	w.recordMebiBytes(name, value)
	return err
}

func (w *IngressClientWrapper) SendMetric(name string, value int, opts ...loggregator.EmitGaugeOption) error {
	err := w.inner.SendMetric(name, value, opts...)
	w.recordMetric(name, value)
	return err
}

func (w *IngressClientWrapper) SendDuration(name string, value time.Duration, opts ...loggregator.EmitGaugeOption) error {
	err := w.inner.SendDuration(name, value, opts...)
	w.recordDuration(name, value)
	return err
}

// ---- Pass-through methods ----

func (w *IngressClientWrapper) SendBytesPerSecond(name string, value float64) error {
	return w.inner.SendBytesPerSecond(name, value)
}

func (w *IngressClientWrapper) SendRequestsPerSecond(name string, value float64) error {
	return w.inner.SendRequestsPerSecond(name, value)
}

func (w *IngressClientWrapper) IncrementCounter(name string) error {
	return w.inner.IncrementCounter(name)
}

func (w *IngressClientWrapper) IncrementCounterWithDelta(name string, value uint64) error {
	return w.inner.IncrementCounterWithDelta(name, value)
}

func (w *IngressClientWrapper) SendAppLog(message, sourceType string, tags map[string]string) error {
	return w.inner.SendAppLog(message, sourceType, tags)
}

func (w *IngressClientWrapper) SendAppErrorLog(message, sourceType string, tags map[string]string) error {
	return w.inner.SendAppErrorLog(message, sourceType, tags)
}

func (w *IngressClientWrapper) SendAppMetrics(metrics loggingclient.ContainerMetric) error {
	return w.inner.SendAppMetrics(metrics)
}

func (w *IngressClientWrapper) SendAppLogRate(rate, rateLimit float64, tags map[string]string) error {
	return w.inner.SendAppLogRate(rate, rateLimit, tags)
}

func (w *IngressClientWrapper) SendSpikeMetrics(metrics loggingclient.SpikeMetric) error {
	return w.inner.SendSpikeMetrics(metrics)
}

func (w *IngressClientWrapper) SendComponentMetric(name string, value float64, unit string) error {
	return w.inner.SendComponentMetric(name, value, unit)
}

// ---- Internal recording logic ----

func (w *IngressClientWrapper) recordMebiBytes(name string, valueMiB int) {
	bytes := float64(valueMiB) * mebibytesToBytes
	bit, ok := metricNameToBit[name]
	if !ok {
		return
	}

	switch bit {
	case bitCapTotalMem:
		w.pending.CapacityTotalMemoryBytes = bytes
	case bitCapRemMem:
		w.pending.CapacityRemainingMemoryBytes = bytes
	case bitCapAllocMem:
		w.pending.CapacityAllocatedMemoryBytes = bytes
	case bitCapTotalDisk:
		w.pending.CapacityTotalDiskBytes = bytes
	case bitCapRemDisk:
		w.pending.CapacityRemainingDiskBytes = bytes
	case bitCapAllocDisk:
		w.pending.CapacityAllocatedDiskBytes = bytes
	case bitContainerUsageMem:
		w.pending.ContainerUsageMemoryBytes = bytes
	case bitContainerUsageDisk:
		w.pending.ContainerUsageDiskBytes = bytes
	}

	w.seen |= bit
	w.maybePublish()
}

func (w *IngressClientWrapper) recordMetric(name string, value int) {
	switch name {
	case "CapacityTotalContainers":
		w.pending.CapacityTotalContainers = float64(value)
		w.seen |= bitCapTotalContainers
	case "CapacityRemainingContainers":
		w.pending.CapacityRemainingContainers = float64(value)
		w.seen |= bitCapRemContainers
	case "ContainerCount":
		w.pending.ContainersAll = float64(value)
		w.seen |= bitContainerCountAll
	case "StartingContainerCount":
		w.pending.ContainersStarting = float64(value)
		w.seen |= bitContainerCountStarting
	case "StrandedEvacuatingActualLRPs":
		w.metrics.StrandedEvacuatingActualLRPs.Set(float64(value))
	}

	w.maybePublish()
}

func (w *IngressClientWrapper) recordDuration(name string, value time.Duration) {
	switch name {
	case "RepBulkSyncDuration":
		w.metrics.BulkSyncDuration.Observe(value.Seconds())
	}
}

// maybePublish checks whether all expected capacity/usage metrics have been
// received in this tick and publishes the observation atomically.
func (w *IngressClientWrapper) maybePublish() {
	if w.seen == expectedMask {
		obs := *w.pending // copy
		w.metrics.SetObservation(&obs)
		w.pending = &Observation{}
		w.seen = 0
	}
}

// MarkSampleError should be called when the executor reporter encounters
// an error.  It clears any partial pending observation and increments
// the error counter.
func (w *IngressClientWrapper) MarkSampleError() {
	w.pending = &Observation{}
	w.seen = 0
	w.metrics.ClearObservation()
	w.metrics.SampleErrors.Inc()
}
