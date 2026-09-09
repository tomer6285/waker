package sleeper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/tomer/waker/pkg/config"
)

func TestSleepAgent(t *testing.T) {
	receivedAuth := ""
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sleep" && r.Method == "POST" {
			receivedAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"suspending"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	u, _ := url.Parse(ts.URL)
	port, _ := strconv.Atoi(u.Port())

	host := &config.HostConfig{
		Name: "agent-box",
		IP:   u.Hostname(),
		OnSleep: &config.SleepAction{
			Type:  "agent",
			Port:  port,
			Token: "secret123",
		},
	}

	sleeper := NewSleeper()
	err := sleeper.Sleep(context.Background(), host)
	if err != nil {
		t.Fatalf("Sleep failed: %v", err)
	}

	if receivedAuth != "Bearer secret123" {
		t.Errorf("expected Bearer secret123, got %q", receivedAuth)
	}
}
