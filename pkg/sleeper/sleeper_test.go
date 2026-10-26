package sleeper

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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

func TestAgentRequiresClientToken(t *testing.T) {
	h := &config.HostConfig{IP: "127.0.0.1", OnSleep: &config.SleepAction{Type: "agent"}}
	if err := NewSleeper().Sleep(context.Background(), h); err == nil {
		t.Fatal("missing token accepted")
	}
}

func TestSSHSleepReportsAuthenticationError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX test double")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\necho 'Permission denied' >&2\nexit 255\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	err := NewSleeper().Sleep(context.Background(), &config.HostConfig{IP: "desktop", OnSleep: &config.SleepAction{Type: "ssh"}})
	if err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("SSH failure swallowed: %v", err)
	}
}

func TestSSHSuspendDisconnectRequiresAcknowledgement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX test double")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	host := &config.HostConfig{IP: "127.0.0.1", Checks: []config.CheckConfig{{Type: "tcp", Port: port}}}
	cases := []struct {
		script    string
		wantError bool
	}{
		{"echo WAKER_SUSPEND_REQUESTED; exit 255", false},
		{"echo WAKER_SUSPEND_REQUESTED; echo 'Suspend denied' >&2; exit 1", true},
		{"echo 'Permission denied' >&2; exit 255", true},
	}
	for _, tc := range cases {
		if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\n"+tc.script+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		err := NewSleeper().Sleep(context.Background(), host)
		if (err != nil) != tc.wantError {
			t.Fatalf("%s: %v", tc.script, err)
		}
	}
}
