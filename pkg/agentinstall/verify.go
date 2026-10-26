package agentinstall

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"
)

// Verify checks authorization without requesting suspension. Never follow redirects
// with a provisioning credential, and bypass HTTP proxies for a direct host check.
func Verify(ctx context.Context, host string, port int, token string) error {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	url := "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/verify"
	var last error
	for {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			if resp.StatusCode == http.StatusUnauthorized {
				return fmt.Errorf("agent rejected the provisioned credential")
			}
			last = fmt.Errorf("agent verification returned HTTP %d", resp.StatusCode)
		} else {
			last = fmt.Errorf("agent is not reachable")
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %v (check agent logs, bind address, and firewall)", ctx.Err(), last)
		case <-time.After(500 * time.Millisecond):
		}
	}
}
