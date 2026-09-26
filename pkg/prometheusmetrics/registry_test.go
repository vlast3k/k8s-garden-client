package prometheusmetrics_test

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"code.cloudfoundry.org/k8s-garden-client/pkg/prometheusmetrics"
)

func TestValidateLoopbackAddress(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		wantErr bool
	}{
		{"loopback IPv4", "127.0.0.1:9090", false},
		{"loopback IPv6", "[::1]:9090", false},
		{"unspecified IPv4", "0.0.0.0:9090", true},
		{"unspecified IPv6", "[::]:9090", true},
		{"pod IP", "10.0.1.5:9090", true},
		{"empty host", ":9090", true},
		{"no port", "127.0.0.1", true},
		{"hostname", "localhost:9090", true},
		{"wildcard", "0.0.0.0:0", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := prometheusmetrics.ValidateLoopbackAddress(tt.addr)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateLoopbackAddress(%q) error = %v, wantErr = %v", tt.addr, err, tt.wantErr)
			}
		})
	}
}

func TestRepMetrics_NoObservation(t *testing.T) {
	m := prometheusmetrics.NewRepMetrics()

	// With no observation set, scraping should return only the self-registered
	// metrics (histogram, gauge, counter) and none of the capacity gauges.
	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}

	for _, f := range families {
		if strings.HasPrefix(f.GetName(), "cf_rep_capacity_") {
			t.Errorf("expected no capacity metrics before observation, got %s", f.GetName())
		}
		if f.GetName() == "cf_rep_containers" {
			t.Errorf("expected no container count before observation, got cf_rep_containers")
		}
	}
}

func TestRepMetrics_WithObservation(t *testing.T) {
	m := prometheusmetrics.NewRepMetrics()

	obs := &prometheusmetrics.Observation{
		CapacityTotalMemoryBytes:     8 * 1024 * 1024 * 1024, // 8 GiB
		CapacityRemainingMemoryBytes: 4 * 1024 * 1024 * 1024,
		CapacityAllocatedMemoryBytes: 4 * 1024 * 1024 * 1024,
		CapacityTotalDiskBytes:       100 * 1024 * 1024 * 1024,
		CapacityRemainingDiskBytes:   50 * 1024 * 1024 * 1024,
		CapacityAllocatedDiskBytes:   50 * 1024 * 1024 * 1024,
		CapacityTotalContainers:      250,
		CapacityRemainingContainers:  200,
		ContainerUsageMemoryBytes:    2 * 1024 * 1024 * 1024,
		ContainerUsageDiskBytes:      10 * 1024 * 1024 * 1024,
		ContainersAll:                50,
		ContainersStarting:           3,
	}
	m.SetObservation(obs)

	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}

	expected := map[string]float64{
		"cf_rep_capacity_total_memory_bytes":     8 * 1024 * 1024 * 1024,
		"cf_rep_capacity_remaining_memory_bytes": 4 * 1024 * 1024 * 1024,
		"cf_rep_capacity_allocated_memory_bytes": 4 * 1024 * 1024 * 1024,
		"cf_rep_capacity_total_disk_bytes":       100 * 1024 * 1024 * 1024,
		"cf_rep_capacity_remaining_disk_bytes":   50 * 1024 * 1024 * 1024,
		"cf_rep_capacity_allocated_disk_bytes":   50 * 1024 * 1024 * 1024,
		"cf_rep_capacity_total_containers":       250,
		"cf_rep_capacity_remaining_containers":   200,
		"cf_rep_container_usage_memory_bytes":    2 * 1024 * 1024 * 1024,
		"cf_rep_container_usage_disk_bytes":      10 * 1024 * 1024 * 1024,
	}

	found := make(map[string]bool)
	for _, f := range families {
		name := f.GetName()
		if v, ok := expected[name]; ok {
			got := f.GetMetric()[0].GetGauge().GetValue()
			if got != v {
				t.Errorf("%s = %f, want %f", name, got, v)
			}
			found[name] = true
		}
		if name == "cf_rep_containers" {
			for _, m := range f.GetMetric() {
				for _, lp := range m.GetLabel() {
					if lp.GetName() == "state" {
						switch lp.GetValue() {
						case "all":
							if m.GetGauge().GetValue() != 50 {
								t.Errorf("cf_rep_containers{state=all} = %f, want 50", m.GetGauge().GetValue())
							}
						case "starting":
							if m.GetGauge().GetValue() != 3 {
								t.Errorf("cf_rep_containers{state=starting} = %f, want 3", m.GetGauge().GetValue())
							}
						default:
							t.Errorf("unexpected state label value: %s", lp.GetValue())
						}
					}
				}
			}
			found["cf_rep_containers"] = true
		}
	}

	for name := range expected {
		if !found[name] {
			t.Errorf("expected metric %s not found in Gather output", name)
		}
	}
	if !found["cf_rep_containers"] {
		t.Error("expected metric cf_rep_containers not found")
	}
}

func TestRepMetrics_ClearObservation(t *testing.T) {
	m := prometheusmetrics.NewRepMetrics()

	obs := &prometheusmetrics.Observation{
		CapacityTotalMemoryBytes: 1024,
	}
	m.SetObservation(obs)

	// Verify it's present.
	families, _ := m.Registry().Gather()
	foundBefore := false
	for _, f := range families {
		if f.GetName() == "cf_rep_capacity_total_memory_bytes" {
			foundBefore = true
		}
	}
	if !foundBefore {
		t.Fatal("expected capacity metric to be present before clear")
	}

	// Clear and verify it's gone.
	m.ClearObservation()
	families, _ = m.Registry().Gather()
	for _, f := range families {
		if strings.HasPrefix(f.GetName(), "cf_rep_capacity_") {
			t.Errorf("expected no capacity metrics after clear, got %s", f.GetName())
		}
	}
}

func TestMetricsServer_LoopbackOnly(t *testing.T) {
	m := prometheusmetrics.NewRepMetrics()
	obs := &prometheusmetrics.Observation{
		CapacityTotalMemoryBytes:     1024,
		CapacityRemainingMemoryBytes: 512,
		CapacityAllocatedMemoryBytes: 512,
		CapacityTotalDiskBytes:       2048,
		CapacityRemainingDiskBytes:   1024,
		CapacityAllocatedDiskBytes:   1024,
		CapacityTotalContainers:      10,
		CapacityRemainingContainers:  5,
		ContainerUsageMemoryBytes:    256,
		ContainerUsageDiskBytes:      128,
		ContainersAll:                5,
		ContainersStarting:           1,
	}
	m.SetObservation(obs)

	// Start the server on a random loopback port.
	server := prometheusmetrics.NewMetricsServer("127.0.0.1:0", m)
	signals := make(chan os.Signal, 1)
	ready := make(chan struct{})
	errCh := make(chan error, 1)

	// We need to extract the actual port. Use the ifrit runner directly.
	// For a simpler test, start on a fixed port (unlikely to collide).
	addr := "127.0.0.1:19876"
	server = prometheusmetrics.NewMetricsServer(addr, m)

	go func() {
		errCh <- server.Run(signals, ready)
	}()

	select {
	case <-ready:
	case err := <-errCh:
		t.Fatalf("server failed to start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not become ready in 5s")
	}

	defer func() {
		signals <- os.Interrupt
		<-errCh
	}()

	resp, err := http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("GET /metrics status = %d, want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Verify key metric families are present.
	for _, needle := range []string{
		"cf_rep_capacity_total_memory_bytes",
		"cf_rep_capacity_remaining_containers",
		"cf_rep_containers",
		"cf_rep_bulk_sync_duration_seconds",
		"cf_rep_stranded_evacuating_actual_lrps",
		"cf_rep_sample_errors_total",
	} {
		if !strings.Contains(bodyStr, needle) {
			t.Errorf("expected %q in /metrics output", needle)
		}
	}

	// Verify state labels.
	if !strings.Contains(bodyStr, `state="all"`) {
		t.Error("expected state=\"all\" label in /metrics output")
	}
	if !strings.Contains(bodyStr, `state="starting"`) {
		t.Error("expected state=\"starting\" label in /metrics output")
	}
}
