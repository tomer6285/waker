package main

import (
	"crypto/subtle"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func suspendSystem() error {
	switch runtime.GOOS {
	case "linux":
		return exec.Command("systemctl", "suspend").Run()
	case "darwin":
		return exec.Command("osascript", "-e", "tell application \"System Events\" to sleep").Run()
	case "windows":
		return exec.Command("rundll32.exe", "powrprof.dll,SetSuspendState", "0,1,0").Run()
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

// newHandler is a sleep-only endpoint on the target, never a wake relay.
func newHandler(expectedToken string, suspend func() error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/sleep", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		auth := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(auth, "Bearer ")
		if expectedToken == "" || !ok || subtle.ConstantTimeCompare([]byte(token), []byte(expectedToken)) != 1 {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"suspending"}`))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		go func() {
			if err := suspend(); err != nil {
				log.Printf("Failed to suspend system: %v", err)
			}
		}()
	})
	return mux
}

func main() {
	port := flag.Int("port", 9876, "Port to listen on")
	bind := flag.String("bind", "127.0.0.1", "Bind IP (use this target's Tailscale IP for remote sleep)")
	tokenFlag := flag.String("token", "", "Bearer token (prefer WAKER_AGENT_TOKEN)")
	flag.Parse()
	expectedToken := *tokenFlag
	if expectedToken == "" {
		expectedToken = os.Getenv("WAKER_AGENT_TOKEN")
	}
	if expectedToken == "" {
		log.Fatal("WAKER_AGENT_TOKEN or --token is required")
	}
	addr := net.JoinHostPort(*bind, strconv.Itoa(*port))
	log.Printf("waker-agent sleep endpoint listening on http://%s", addr)
	server := &http.Server{
		Addr: addr, Handler: newHandler(expectedToken, suspendSystem),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("waker-agent server failed: %v", err)
	}
}
