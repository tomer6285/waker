package agentinstall

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

type Remote struct {
	Target   string
	Args     []string
	Out, Err io.Writer
	In       io.Reader
}

func (r Remote) command(ctx context.Context, script string, tty bool) *exec.Cmd {
	args := append([]string(nil), r.Args...)
	args = append(args, "-o", "BatchMode=yes", "-o", "ConnectTimeout=10")
	if tty {
		args = append(args, "-t")
	}
	args = append(args, "--", r.Target, "/bin/sh -c "+ShellQuote(script))
	return exec.CommandContext(ctx, "ssh", args...)
}
func (r Remote) output(ctx context.Context, script string) ([]byte, error) {
	cmd := r.command(ctx, script, false)
	cmd.Stderr = r.Err
	b, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("SSH command failed: %w", err)
	}
	return b, nil
}
func ParsePlatform(out string) (string, string, error) {
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return "", "", fmt.Errorf("could not detect remote OS/architecture; SSH setup requires Linux or macOS with a POSIX shell")
	}
	platform, arch := strings.ToLower(fields[0]), fields[1]
	if platform != "linux" && platform != "darwin" {
		return "", "", fmt.Errorf("SSH setup supports Linux and macOS; run waker-agent setup locally on Windows")
	}
	switch arch {
	case "x86_64", "amd64":
		arch = "amd64"
	case "aarch64", "arm64":
		arch = "arm64"
	default:
		return "", "", fmt.Errorf("unsupported remote architecture %q", arch)
	}
	return platform, arch, nil
}
func (r Remote) Probe(ctx context.Context) (string, string, error) {
	if r.Target == "" || strings.HasPrefix(r.Target, "-") || strings.ContainsAny(r.Target, "\r\n\x00") {
		return "", "", fmt.Errorf("invalid SSH target")
	}
	b, err := r.output(ctx, "uname -s && uname -m")
	if err != nil {
		return "", "", err
	}
	return ParsePlatform(string(b))
}
func (r Remote) DetectBind(ctx context.Context) (string, error) {
	b, err := r.output(ctx, `if command -v tailscale >/dev/null 2>&1; then tailscale ip -4; elif [ -x /Applications/Tailscale.app/Contents/MacOS/Tailscale ]; then /Applications/Tailscale.app/Contents/MacOS/Tailscale ip -4; else exit 1; fi`)
	if err != nil {
		return "", fmt.Errorf("could not detect target Tailscale address; use --bind with a specific target IP: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

var remoteDir = regexp.MustCompile(`^/tmp/waker-agent\.[A-Za-z0-9]+$`)

// Install stages files in a private remote directory. Credentials travel through
// SSH stdin, never command arguments. Only the service installation gets a TTY.
func (r Remote) Install(ctx context.Context, binary, platform string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	executable, err := os.ReadFile(binary)
	if err != nil {
		return err
	}
	provision, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	for _, f := range []struct {
		name string
		data []byte
		mode int64
	}{{"waker-agent", executable, 0700}, {"provision.json", provision, 0600}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: f.mode, Size: int64(len(f.data))}); err != nil {
			return err
		}
		if _, err := tw.Write(f.data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	b, err := r.output(ctx, "umask 077; mktemp -d /tmp/waker-agent.XXXXXXXXXX")
	if err != nil {
		return err
	}
	dir := strings.TrimSpace(string(b))
	if !remoteDir.MatchString(dir) {
		return fmt.Errorf("remote returned an unexpected staging directory")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := r.output(cleanup, "rm -rf -- "+ShellQuote(dir)); err != nil {
			fmt.Fprintln(r.Err, "Could not remove remote staging directory:", dir)
		}
	}()
	upload := r.command(ctx, "umask 077; tar -xf - -C "+ShellQuote(dir), false)
	upload.Stdin, upload.Stdout, upload.Stderr = &archive, r.Out, r.Err
	if err := upload.Run(); err != nil {
		return fmt.Errorf("upload agent: %w", err)
	}
	command := ShellQuote(dir+"/waker-agent") + " setup --provision-file " + ShellQuote(dir+"/provision.json") + " --no-pair"
	if platform == "linux" {
		command = "if [ \"$(id -u)\" = 0 ]; then exec " + command + "; else exec sudo -- " + command + "; fi"
	}
	install := r.command(ctx, command, platform == "linux")
	install.Stdin, install.Stdout, install.Stderr = r.In, r.Out, r.Err
	if err := install.Run(); err != nil {
		return fmt.Errorf("remote setup failed (Linux needs sudo; macOS needs a logged-in desktop session): %w", err)
	}
	return nil
}
