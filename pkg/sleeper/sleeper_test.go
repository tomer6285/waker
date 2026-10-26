package sleeper

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
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

func TestSSHSleepRemoteShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX test double")
	}
	for _, shell := range []string{"sh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			shellPath, err := exec.LookPath(shell)
			if err != nil {
				t.Skipf("%s unavailable: %v", shell, err)
			}
			for _, exitCode := range []string{"0", "1"} {
				t.Run("suspend_exit_"+exitCode, func(t *testing.T) {
					dir := t.TempDir()
					// Emulate sshd passing the remote command to the login shell.
					sshScript := "#!/bin/sh\nfor remote do :; done\nexec \"$WAKER_TEST_REMOTE_SHELL\" "
					if shell == "fish" {
						sshScript += "--no-config "
					}
					sshScript += "-c \"$remote\"\n"
					// Restrict PATH so the real suspend command can never run.
					for name, script := range map[string]string{
						"ssh": sshScript,
						"systemctl": "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$WAKER_TEST_SUSPEND_LOG\"\n" +
							"echo 'mock suspend result' >&2\nexit \"$WAKER_TEST_SUSPEND_EXIT\"\n",
					} {
						if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
							t.Fatal(err)
						}
					}
					logPath := filepath.Join(dir, "suspend.log")
					t.Setenv("PATH", dir)
					t.Setenv("WAKER_TEST_REMOTE_SHELL", shellPath)
					t.Setenv("WAKER_TEST_SUSPEND_LOG", logPath)
					t.Setenv("WAKER_TEST_SUSPEND_EXIT", exitCode)
					err := NewSleeper().Sleep(context.Background(), &config.HostConfig{IP: "desktop"})
					if exitCode == "0" && err != nil {
						t.Fatalf("sleep failed: %v", err)
					}
					if exitCode != "0" && (err == nil || !strings.Contains(err.Error(), "mock suspend result")) {
						t.Fatalf("suspend failure not reported: %v", err)
					}
					out, err := os.ReadFile(logPath)
					if err != nil {
						t.Fatal(err)
					}
					if string(out) != "suspend\n" {
						t.Fatalf("unexpected systemctl arguments: %q", out)
					}
				})
			}
		})
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
