// Package distribution downloads checksum-verified binaries from Waker releases.
package distribution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var Version = "dev" // Set by release builds using -ldflags.
const BaseURL = "https://github.com/tomer6285/waker/releases"

var validRelease = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:[-.][A-Za-z0-9.-]+)?$`)

func AssetName(binary, platform, arch string) (string, error) {
	if binary != "waker" && binary != "waker-agent" {
		return "", fmt.Errorf("unknown binary")
	}
	if platform != "linux" && platform != "darwin" && platform != "windows" {
		return "", fmt.Errorf("unsupported OS %q", platform)
	}
	if arch != "amd64" && arch != "arm64" {
		return "", fmt.Errorf("unsupported architecture %q", arch)
	}
	name := binary + "_" + platform + "_" + arch
	if platform == "windows" {
		name += ".exe"
	}
	return name, nil
}

func Download(ctx context.Context, binary, platform, arch, release, dir string) (string, error) {
	return download(ctx, &http.Client{Timeout: 3 * time.Minute}, BaseURL, binary, platform, arch, release, dir)
}
func download(ctx context.Context, client *http.Client, base, binary, platform, arch, release, dir string) (string, error) {
	name, err := AssetName(binary, platform, arch)
	if err != nil {
		return "", err
	}
	prefix := base + "/latest/download/"
	if release != "latest" {
		if !validRelease.MatchString(release) {
			return "", fmt.Errorf("release must be latest or a version such as v1.0.0")
		}
		prefix = base + "/download/" + release + "/"
	}
	get := func(name string, limit int64) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", prefix+name, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("release download returned HTTP %d for %s; publish a release or use --agent-binary", resp.StatusCode, name)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if int64(len(b)) > limit {
			return nil, fmt.Errorf("release asset exceeds size limit")
		}
		return b, err
	}
	sums, err := get("checksums.txt", 1024*1024)
	if err != nil {
		return "", err
	}
	expected := ""
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			expected = fields[0]
			break
		}
	}
	if len(expected) != 64 {
		return "", fmt.Errorf("release has no valid checksum for %s", name)
	}
	b, err := get(name, 128*1024*1024)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != expected {
		return "", fmt.Errorf("checksum mismatch for %s", name)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, b, 0700); err != nil {
		return "", err
	}
	return path, nil
}
