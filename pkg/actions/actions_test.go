package actions

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tomer/waker/pkg/config"
)

func TestCustomAction(t *testing.T) {
	out := &bytes.Buffer{}
	runner := NewRunner(out, out, nil)

	host := &config.HostConfig{
		Name: "test-box",
		IP:   "10.0.0.99",
		MAC:  "AA:BB:CC:DD:EE:FF",
		OnConnect: &config.ConnectAction{
			Type: "custom",
			Run:  "echo hello $WAKER_HOST_NAME",
		},
	}

	err := runner.Connect(context.Background(), host, nil, false)
	if err != nil {
		t.Fatalf("custom action failed: %v", err)
	}

	if !strings.Contains(out.String(), "hello test-box") {
		t.Errorf("expected 'hello test-box' in output, got %q", out.String())
	}
}
