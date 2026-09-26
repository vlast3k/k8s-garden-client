package prometheusmetrics_test

import (
	"strings"
	"testing"
	"time"

	loggingclient "code.cloudfoundry.org/diego-logging-client"
	loggregator "code.cloudfoundry.org/go-loggregator/v9"
	"code.cloudfoundry.org/k8s-garden-client/pkg/prometheusmetrics"
)

// fakeIngressClient implements loggingclient.IngressClient as a no-op.
type fakeIngressClient struct{}

func (f *fakeIngressClient) SendDuration(string, time.Duration, ...loggregator.EmitGaugeOption) error {
	return nil
}
func (f *fakeIngressClient) SendMebiBytes(string, int, ...loggregator.EmitGaugeOption) error {
	return nil
}
func (f *fakeIngressClient) SendMetric(string, int, ...loggregator.EmitGaugeOption) error {
	return nil
}
func (f *fakeIngressClient) SendBytesPerSecond(string, float64) error       { return nil }
func (f *fakeIngressClient) SendRequestsPerSecond(string, float64) error    { return nil }
func (f *fakeIngressClient) IncrementCounter(string) error                  { return nil }
func (f *fakeIngressClient) IncrementCounterWithDelta(string, uint64) error { return nil }
func (f *fakeIngressClient) SendAppLog(string, string, map[string]string) error {
	return nil
}
func (f *fakeIngressClient) SendAppErrorLog(string, string, map[string]string) error {
	return nil
}
func (f *fakeIngressClient) SendAppMetrics(loggingclient.ContainerMetric) error { return nil }
func (f *fakeIngressClient) SendAppLogRate(float64, float64, map[string]string) error {
	return nil
}
func (f *fakeIngressClient) SendSpikeMetrics(loggingclient.SpikeMetric) error { return nil }
func (f *fakeIngressClient) SendComponentMetric(string, float64, string) error {
	return nil
}

// emitFullReporterTick simulates one complete executor reporter tick
// by calling all 12 capacity/usage metrics in the order the upstream
// reporter emits them.
func emitFullReporterTick(w *prometheusmetrics.IngressClientWrapper) {
	w.SendMebiBytes("CapacityTotalMemory", 8192)       // 8 GiB
	w.SendMebiBytes("CapacityRemainingMemory", 4096)    // 4 GiB
	w.SendMebiBytes("CapacityAllocatedMemory", 4096)    // 4 GiB
	w.SendMebiBytes("CapacityTotalDisk", 102400)        // 100 GiB
	w.SendMebiBytes("CapacityRemainingDisk", 51200)     // 50 GiB
	w.SendMebiBytes("CapacityAllocatedDisk", 51200)     // 50 GiB
	w.SendMebiBytes("ContainerUsageMemory", 2048)       // 2 GiB
	w.SendMebiBytes("ContainerUsageDisk", 10240)        // 10 GiB
	w.SendMetric("CapacityTotalContainers", 250)
	w.SendMetric("CapacityRemainingContainers", 200)
	w.SendMetric("ContainerCount", 50)
	w.SendMetric("StartingContainerCount", 3)
}

func TestIngressClientWrapper_FullTick(t *testing.T) {
	m := prometheusmetrics.NewRepMetrics()
	w := prometheusmetrics.NewIngressClientWrapper(&fakeIngressClient{}, m)

	emitFullReporterTick(w)

	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	want := map[string]float64{
		"cf_rep_capacity_total_memory_bytes":     8192 * 1024 * 1024,
		"cf_rep_capacity_remaining_memory_bytes": 4096 * 1024 * 1024,
		"cf_rep_capacity_allocated_memory_bytes": 4096 * 1024 * 1024,
		"cf_rep_capacity_total_disk_bytes":       102400 * 1024 * 1024,
		"cf_rep_capacity_remaining_disk_bytes":   51200 * 1024 * 1024,
		"cf_rep_capacity_allocated_disk_bytes":   51200 * 1024 * 1024,
		"cf_rep_container_usage_memory_bytes":    2048 * 1024 * 1024,
		"cf_rep_container_usage_disk_bytes":      10240 * 1024 * 1024,
		"cf_rep_capacity_total_containers":       250,
		"cf_rep_capacity_remaining_containers":   200,
	}

	found := make(map[string]bool)
	for _, f := range families {
		name := f.GetName()
		if v, ok := want[name]; ok {
			got := f.GetMetric()[0].GetGauge().GetValue()
			if got != v {
				t.Errorf("%s = %f, want %f", name, got, v)
			}
			found[name] = true
		}
	}

	for name := range want {
		if !found[name] {
			t.Errorf("missing metric %s", name)
		}
	}
}

func TestIngressClientWrapper_MiBToBytes(t *testing.T) {
	m := prometheusmetrics.NewRepMetrics()
	w := prometheusmetrics.NewIngressClientWrapper(&fakeIngressClient{}, m)

	emitFullReporterTick(w)

	families, _ := m.Registry().Gather()
	for _, f := range families {
		if f.GetName() == "cf_rep_capacity_total_memory_bytes" {
			got := f.GetMetric()[0].GetGauge().GetValue()
			// 8192 MiB = 8192 * 1024 * 1024 bytes = 8589934592
			if got != 8589934592 {
				t.Errorf("MiB to bytes conversion: got %f, want 8589934592", got)
			}
			return
		}
	}
	t.Error("cf_rep_capacity_total_memory_bytes not found")
}

func TestIngressClientWrapper_BulkSyncDuration(t *testing.T) {
	m := prometheusmetrics.NewRepMetrics()
	w := prometheusmetrics.NewIngressClientWrapper(&fakeIngressClient{}, m)

	w.SendDuration("RepBulkSyncDuration", 2500*time.Millisecond)

	families, _ := m.Registry().Gather()
	for _, f := range families {
		if f.GetName() == "cf_rep_bulk_sync_duration_seconds" {
			h := f.GetMetric()[0].GetHistogram()
			if h.GetSampleCount() != 1 {
				t.Errorf("histogram count = %d, want 1", h.GetSampleCount())
			}
			if h.GetSampleSum() != 2.5 {
				t.Errorf("histogram sum = %f, want 2.5", h.GetSampleSum())
			}
			return
		}
	}
	t.Error("cf_rep_bulk_sync_duration_seconds not found")
}

func TestIngressClientWrapper_StrandedEvacuating(t *testing.T) {
	m := prometheusmetrics.NewRepMetrics()
	w := prometheusmetrics.NewIngressClientWrapper(&fakeIngressClient{}, m)

	w.SendMetric("StrandedEvacuatingActualLRPs", 7)

	families, _ := m.Registry().Gather()
	for _, f := range families {
		if f.GetName() == "cf_rep_stranded_evacuating_actual_lrps" {
			got := f.GetMetric()[0].GetGauge().GetValue()
			if got != 7 {
				t.Errorf("stranded = %f, want 7", got)
			}
			return
		}
	}
	t.Error("cf_rep_stranded_evacuating_actual_lrps not found")
}

func TestIngressClientWrapper_MarkSampleError(t *testing.T) {
	m := prometheusmetrics.NewRepMetrics()
	w := prometheusmetrics.NewIngressClientWrapper(&fakeIngressClient{}, m)

	// First, set a good observation.
	emitFullReporterTick(w)

	// Verify capacity metrics are present.
	families, _ := m.Registry().Gather()
	foundBefore := false
	for _, f := range families {
		if f.GetName() == "cf_rep_capacity_total_memory_bytes" {
			foundBefore = true
		}
	}
	if !foundBefore {
		t.Fatal("expected capacity metric before error")
	}

	// Mark an error.
	w.MarkSampleError()

	// Capacity metrics should be gone.
	families, _ = m.Registry().Gather()
	for _, f := range families {
		if strings.HasPrefix(f.GetName(), "cf_rep_capacity_") {
			t.Errorf("expected no capacity metrics after error, got %s", f.GetName())
		}
	}

	// Error counter should be 1.
	for _, f := range families {
		if f.GetName() == "cf_rep_sample_errors_total" {
			got := f.GetMetric()[0].GetCounter().GetValue()
			if got != 1 {
				t.Errorf("error counter = %f, want 1", got)
			}
			return
		}
	}
	t.Error("cf_rep_sample_errors_total not found")
}

func TestIngressClientWrapper_PartialTickDoesNotPublish(t *testing.T) {
	m := prometheusmetrics.NewRepMetrics()
	w := prometheusmetrics.NewIngressClientWrapper(&fakeIngressClient{}, m)

	// Send only some metrics (incomplete tick).
	w.SendMebiBytes("CapacityTotalMemory", 8192)
	w.SendMebiBytes("CapacityRemainingMemory", 4096)

	// Should not have published yet.
	families, _ := m.Registry().Gather()
	for _, f := range families {
		if strings.HasPrefix(f.GetName(), "cf_rep_capacity_") {
			t.Errorf("expected no capacity metrics from partial tick, got %s", f.GetName())
		}
	}
}

func TestIngressClientWrapper_UnrelatedMetricsPassThrough(t *testing.T) {
	m := prometheusmetrics.NewRepMetrics()
	w := prometheusmetrics.NewIngressClientWrapper(&fakeIngressClient{}, m)

	// Unrelated metric names should not affect Prometheus state.
	w.SendMebiBytes("SomeOtherMetric", 100)
	w.SendMetric("SomeOtherCount", 42)
	w.SendDuration("SomeOtherDuration", time.Second)

	families, _ := m.Registry().Gather()
	for _, f := range families {
		if strings.HasPrefix(f.GetName(), "cf_rep_capacity_") {
			t.Errorf("unrelated metric should not produce capacity gauge, got %s", f.GetName())
		}
	}
}
