package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
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

func main() {
	port := flag.Int("port", 9876, "Port to listen on")
	bind := flag.String("bind", "0.0.0.0", "Bind address")
	tokenFlag := flag.String("token", "", "Bearer token for authorization (or set WAKER_AGENT_TOKEN)")
	flag.Parse()

	expectedToken := *tokenFlag
	if expectedToken == "" {
		expectedToken = os.Getenv("WAKER_AGENT_TOKEN")
	}

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	http.HandleFunc("/sleep", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if expectedToken != "" {
			authHeader := r.Header.Get("Authorization")
			token := strings.TrimPrefix(authHeader, "Bearer ")
			if token != expectedToken {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"suspending"}`))

		// Flush response then suspend in background
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}

		go func() {
			log.Printf("Executing system suspend...")
			if err := suspendSystem(); err != nil {
				log.Printf("Failed to suspend system: %v", err)
			}
		}()
	})

	addr := fmt.Sprintf("%s:%d", *bind, *port)
	log.Printf("waker-agent listening on http://%s", addr)
	if expectedToken != "" {
		log.Printf("Token authentication enabled")
	} else {
		log.Printf("Warning: Running without token authentication")
	}

	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("waker-agent server failed: %v", err)
	}
}
