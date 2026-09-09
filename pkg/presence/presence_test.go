package presence

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomer/waker/pkg/config"
	"github.com/tomer/waker/pkg/store"
)

func TestPresencePoller(t *testing.T) {
	// TCP listener
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer l.Close()

	port := l.Addr().(*net.TCPAddr).Port

	cfg := &config.Config{
		Defaults: config.DefaultsConfig{
			ProbeTimeout: 1 * time.Second,
			PollInterval: 10 * time.Second,
		},
		Hosts: []config.HostConfig{
			{
				Name: "test-host",
				MAC:  "AA:BB:CC:DD:EE:FF",
				IP:   "127.0.0.1",
				Checks: []config.CheckConfig{
					{Type: "tcp", Port: port},
				},
			},
		},
	}

	tmpDir := t.TempDir()
	st, err := store.LoadStore(filepath.Join(tmpDir, "state.json"))
	if err != nil {
		t.Fatalf("LoadStore failed: %v", err)
	}

	poller := NewPoller(cfg, st)
	res := poller.PollOnce(context.Background())

	info, ok := res["test-host"]
	if !ok {
		t.Fatal("expected test-host in poll results")
	}
	if info.Status != StatusOnline {
		t.Errorf("expected online, got %s", info.Status)
	}

	// Now close listener and verify it reports offline (or waking if marked)
	l.Close()
	poller.MarkWaking("test-host")
	res2 := poller.PollOnce(context.Background())
	info2 := res2["test-host"]
	if info2.Status != StatusWaking {
		t.Errorf("expected waking, got %s", info2.Status)
	}
}
