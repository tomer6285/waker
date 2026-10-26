package agentinstall

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{Bind: "100.101.102.103", Port: 9876, Token: strings.Repeat("a", 64)}
}
func testPaths(t *testing.T) Paths {
	d := t.TempDir()
	return Paths{Dir: filepath.Join(d, "config"), Binary: filepath.Join(d, "bin", "waker-agent"), Config: filepath.Join(d, "config", "config.json"), Service: filepath.Join(d, "service", "waker-agent.service"), Log: filepath.Join(d, "agent.log")}
}
func TestInstallRepairAndUninstall(t *testing.T) {
	p := testPaths(t)
	source := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(source, []byte("binary-v1"), 0700); err != nil {
		t.Fatal(err)
	}
	var calls []string
	m := Manager{Platform: "linux", Paths: p, Run: func(name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}}
	cfg := testConfig()
	for i := 0; i < 2; i++ {
		if err := m.Install(source, cfg); err != nil {
			t.Fatal(err)
		}
		got, err := ReadConfig(p.Config)
		if err != nil || got != cfg {
			t.Fatalf("credential not preserved: %v", err)
		}
		info, _ := os.Stat(p.Config)
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatal("config is not private")
		}
	}
	if !strings.Contains(strings.Join(calls, "\n"), "systemctl stop waker-agent") {
		t.Fatal("repair did not stop existing service")
	}
	if err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	// Retained configuration must not make reinstallation fail after removal.
	if err := m.Install(source, cfg); err != nil {
		t.Fatal(err)
	}
	definition, _ := os.ReadFile(p.Service)
	if strings.Contains(string(definition), cfg.Token) {
		t.Fatal("service definition leaked credential")
	}
}
func TestFailedStopDoesNotReplaceInstallation(t *testing.T) {
	p := testPaths(t)
	source := filepath.Join(t.TempDir(), "source")
	_ = os.WriteFile(source, []byte("old"), 0700)
	m := Manager{Platform: "linux", Paths: p, Run: func(string, ...string) error { return nil }}
	if err := m.Install(source, testConfig()); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(source, []byte("new"), 0700)
	m.Run = func(string, ...string) error { return errors.New("denied") }
	if err := m.Install(source, testConfig()); err == nil {
		t.Fatal("stop error swallowed")
	}
	b, _ := os.ReadFile(p.Binary)
	if string(b) != "old" {
		t.Fatal("failed stop replaced binary")
	}
}
func TestPrivateWriteDoesNotFollowSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges")
	}
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	target := filepath.Join(dir, "credential")
	_ = os.WriteFile(victim, []byte("untouched"), 0644)
	if err := os.Symlink(victim, target); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(target, []byte("secret")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(victim)
	if string(b) != "untouched" {
		t.Fatal("followed symlink")
	}
	info, _ := os.Stat(target)
	if info.Mode().Perm() != 0600 {
		t.Fatal("private file has wrong permissions")
	}
}
func TestMacDefinitionEscapesPaths(t *testing.T) {
	p := Paths{Binary: `/Users/A & B/<waker>/agent`, Config: `/Users/A & B/config.json`, Log: `/Users/A & B/log`}
	b := []byte(ServiceDefinition("darwin", p))
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	var values []string
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if start, ok := tok.(xml.StartElement); ok && start.Name.Local == "string" {
			var value string
			if err := dec.DecodeElement(&value, &start); err != nil {
				t.Fatal(err)
			}
			values = append(values, value)
		}
	}
	if len(values) < 7 || values[1] != p.Binary || values[4] != p.Config || values[5] != p.Log {
		t.Fatalf("paths did not survive XML: %q", values)
	}
}
func TestConfigValidationAndToken(t *testing.T) {
	a, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewToken()
	if len(a) != 64 || a == b {
		t.Fatal("invalid generated tokens")
	}
	for _, change := range []func(*Config){func(c *Config) { c.Bind = "0.0.0.0" }, func(c *Config) { c.Bind = "bad" }, func(c *Config) { c.Port = 65536 }, func(c *Config) { c.Token = "short" }} {
		c := testConfig()
		change(&c)
		if c.Validate() == nil {
			t.Fatal("invalid setup accepted")
		}
	}
}
func TestVerifyDoesNotSleepOrFollowRedirects(t *testing.T) {
	var slept, leaked bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/verify" || r.Method != "GET" {
			slept = true
			t.Error("verification requested an action")
		}
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(401)
			return
		}
		http.Redirect(w, r, other.URL, 302)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(u.Port())
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := Verify(ctx, u.Hostname(), port, "good"); err == nil {
		t.Fatal("redirect accepted")
	}
	if slept || leaked {
		t.Fatal("unsafe verification")
	}
	if err := Verify(context.Background(), u.Hostname(), port, "bad"); err == nil {
		t.Fatal("unauthorized check accepted")
	}
}
func TestRemoteTransferAndCleanup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX SSH double")
	}
	dir := t.TempDir()
	capture := filepath.Join(dir, "captured.json")
	argsLog := filepath.Join(dir, "args")
	t.Setenv("WAKER_CAPTURE", capture)
	t.Setenv("WAKER_ARGS", argsLog)
	ssh := `#!/bin/sh
for last do :; done
printf '%s\n' "$last" >> "$WAKER_ARGS"
exec /bin/sh -c "$last"
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(ssh), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	binary := filepath.Join(dir, "agent")
	fake := `#!/bin/sh
[ "$1" = setup ] || exit 2
[ "$2" = --provision-file ] || exit 2
[ "$4" = --no-pair ] || exit 2
cp "$3" "$WAKER_CAPTURE"
printf '%s' "$(dirname "$3")" > "$WAKER_CAPTURE.dir"
exit "${WAKER_FAIL:-0}"
`
	_ = os.WriteFile(binary, []byte(fake), 0700)
	r := Remote{Target: "target", Err: os.Stderr, Out: os.Stdout}
	if err := r.Install(context.Background(), binary, "darwin", testConfig()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var got Config
	if err := json.Unmarshal(b, &got); err != nil || got != testConfig() {
		t.Fatal("provisioned config mismatch")
	}
	staged, _ := os.ReadFile(capture + ".dir")
	if _, err := os.Stat(string(staged)); !os.IsNotExist(err) {
		t.Fatal("staged credentials were not removed")
	}
	t.Setenv("WAKER_FAIL", "1")
	if err := r.Install(context.Background(), binary, "darwin", testConfig()); err == nil {
		t.Fatal("remote installation failure was swallowed")
	}
	staged, _ = os.ReadFile(capture + ".dir")
	if _, err := os.Stat(string(staged)); !os.IsNotExist(err) {
		t.Fatal("failed install retained staged credentials")
	}
	log, _ := os.ReadFile(argsLog)
	if strings.Contains(string(log), testConfig().Token) {
		t.Fatal("credential leaked to command arguments")
	}
}
func TestRemotePlatform(t *testing.T) {
	for input, want := range map[string]string{"Linux\naarch64\n": "linux/arm64", "Darwin\nx86_64\n": "darwin/amd64"} {
		p, a, err := ParsePlatform(input)
		if err != nil || p+"/"+a != want {
			t.Fatal(input, p, a, err)
		}
	}
	for _, input := range []string{"Windows_NT x86_64", "Linux i386", "unexpected banner Linux arm64"} {
		if _, _, err := ParsePlatform(input); err == nil {
			t.Fatal("accepted", input)
		}
	}
}
