package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tomer/waker/pkg/agentinstall"
	"github.com/tomer/waker/pkg/config"
)

func TestSetupPairsAndPersistsOnlyAfterVerification(t *testing.T) {
	for _, authorized := range []bool{true, false} {
		t.Run(strconv.FormatBool(authorized), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "hosts.yaml")
			pair := filepath.Join(dir, "pair.json")
			oldCfg := cfgFile
			cfgFile = path
			defer func() { cfgFile = oldCfg }()
			token := strings.Repeat("b", 64)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/verify" || r.Method != "GET" {
					t.Error("setup requested suspension")
				}
				if !authorized || r.Header.Get("Authorization") != "Bearer "+token {
					w.WriteHeader(401)
				}
			}))
			defer server.Close()
			u, _ := url.Parse(server.URL)
			port, _ := strconv.Atoi(u.Port())
			cfg := &config.Config{Version: 1, Hosts: []config.HostConfig{{Name: "desktop", IP: u.Hostname(), MAC: "AA:BB:CC:DD:EE:FF", OnSleep: &config.SleepAction{Type: "ssh"}}}}
			if err := cfg.Save(path); err != nil {
				t.Fatal(err)
			}
			if err := agentinstall.WriteConfig(pair, agentinstall.Config{Bind: u.Hostname(), Port: port, Token: token}); err != nil {
				t.Fatal(err)
			}
			cmd := newSetupCommand()
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			cmd.SetArgs([]string{"desktop", "--pair-file", pair})
			err := cmd.Execute()
			if (err == nil) != authorized {
				t.Fatalf("unexpected pairing result: %v", err)
			}
			saved, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			host, _ := saved.FindHost("desktop")
			if !authorized {
				if host.OnSleep.Type != "ssh" {
					t.Fatal("failed pairing changed host")
				}
				return
			}
			if host.OnSleep.Type != "agent" || host.OnSleep.TokenFile == "" || host.OnSleep.Token != "" || host.OnSleep.TokenEnv != "" {
				t.Fatal("pairing did not save credential file reference")
			}
			credential, err := os.ReadFile(host.OnSleep.TokenFile)
			if err != nil || strings.TrimSpace(string(credential)) != token {
				t.Fatal("missing protected credential")
			}
			yaml, _ := os.ReadFile(path)
			if strings.Contains(string(yaml), token) || strings.Contains(output.String(), token) {
				t.Fatal("pairing leaked token")
			}
		})
	}
}
