package distribution

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestDownloadChecksumsAndReleaseSelection(t *testing.T) {
	binary := []byte("verified binary")
	sum := sha256.Sum256(binary)
	for _, tc := range []struct {
		name, release, checksum string
		wantErr                 bool
	}{
		{"latest", "latest", fmt.Sprintf("%x", sum), false},
		{"pinned", "v1.2.3", fmt.Sprintf("%x", sum), false},
		{"corrupt", "latest", strings.Repeat("0", 64), true},
		{"invalid-version", "../bad", fmt.Sprintf("%x", sum), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				prefix := "/latest/download/"
				if tc.release == "v1.2.3" {
					prefix = "/download/v1.2.3/"
				}
				if !strings.HasPrefix(r.URL.Path, prefix) {
					t.Error("wrong release path", r.URL.Path)
				}
				if strings.HasSuffix(r.URL.Path, "checksums.txt") {
					fmt.Fprintf(w, "%s  waker-agent_linux_arm64\n", tc.checksum)
				} else {
					w.Write(binary)
				}
			}))
			defer server.Close()
			path, err := download(context.Background(), server.Client(), server.URL, "waker-agent", "linux", "arm64", tc.release, t.TempDir())
			if (err != nil) != tc.wantErr {
				t.Fatalf("unexpected result: %v", err)
			}
			if err == nil {
				b, _ := os.ReadFile(path)
				if string(b) != string(binary) {
					t.Fatal("wrong binary")
				}
			}
		})
	}
}
