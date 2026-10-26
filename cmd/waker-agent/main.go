package main

import (
	"crypto/subtle"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/exec"
	"runtime"
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
	mux.HandleFunc("/verify", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if expectedToken == "" || !ok || subtle.ConstantTimeCompare([]byte(token), []byte(expectedToken)) != 1 {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ready"}`))
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

func serve(bind string, port int, token string) error {
	addr := net.JoinHostPort(bind, fmt.Sprint(port))
	log.Printf("waker-agent sleep endpoint listening on http://%s", addr)
	server := &http.Server{
		Addr: addr, Handler: newHandler(token, suspendSystem),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second,
	}
	return server.ListenAndServe()
}
