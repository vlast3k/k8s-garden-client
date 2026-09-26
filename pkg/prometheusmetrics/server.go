package prometheusmetrics

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/tedsuo/ifrit"
)

const (
	defaultShutdownTimeout = 5 * time.Second
	readHeaderTimeout      = 5 * time.Second
)

// ValidateLoopbackAddress returns an error if addr is not a loopback
// address.  Unspecified ("0.0.0.0", "::"), wildcard, pod-IP, or missing
// host are rejected.
func ValidateLoopbackAddress(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid metrics address %q: %w", addr, err)
	}
	if host == "" {
		return fmt.Errorf("metrics address %q has empty host (unspecified bind rejected)", addr)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("metrics address %q host is not an IP", addr)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("metrics address %q is not loopback (non-loopback bind rejected)", addr)
	}
	return nil
}

// NewMetricsServer returns an ifrit.Runner that serves the given
// Prometheus registry on addr at /metrics.  addr MUST be a loopback
// address; the caller should call ValidateLoopbackAddress before
// constructing the runner (or the runner will reject it at startup).
//
// A bind failure is a fatal rep startup error: the runner returns
// immediately with a non-nil error, preventing the ifrit group from
// reaching the ready state.
func NewMetricsServer(addr string, metrics *RepMetrics) ifrit.Runner {
	return ifrit.RunFunc(func(signals <-chan os.Signal, ready chan<- struct{}) error {
		if err := ValidateLoopbackAddress(addr); err != nil {
			return err
		}

		handler := promhttp.HandlerFor(metrics.Registry(), promhttp.HandlerOpts{
			ErrorHandling: promhttp.HTTPErrorOnError,
		})
		mux := http.NewServeMux()
		mux.Handle("/metrics", handler)

		listener, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("metrics server bind %q: %w", addr, err)
		}

		server := &http.Server{
			Handler:           mux,
			ReadHeaderTimeout: readHeaderTimeout,
		}

		close(ready)

		go func() {
			if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				fmt.Fprintf(os.Stderr, "metrics server Serve: %v\n", err)
			}
		}()

		<-signals

		ctx, cancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
		defer cancel()
		return server.Shutdown(ctx)
	})
}
