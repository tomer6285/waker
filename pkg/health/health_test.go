package health

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/tomer/waker/pkg/config"
)

func TestTCPCheck(t *testing.T) {
	// Start a dummy TCP server
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer l.Close()

	port := l.Addr().(*net.TCPAddr).Port

	host := &config.HostConfig{
		Name: "test-local",
		IP:   "127.0.0.1",
		Checks: []config.CheckConfig{
			{Type: "tcp", Port: port},
		},
	}

	results := CheckHost(context.Background(), host, 2*time.Second)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].Success {
		t.Fatalf("expected TCP check to succeed, error: %s", results[0].Error)
	}

	reachable, lat := IsReachable(results)
	if !reachable {
		t.Fatal("expected reachable true")
	}
	if lat <= 0 {
		t.Errorf("expected positive latency, got %v", lat)
	}
}
