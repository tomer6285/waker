package tailscale

import (
	"context"
	"errors"
	"testing"
)

type mockClient struct {
	isUp       bool
	isUpErr    error
	upCalls    int
	upErr      error
	downCalls  int
	downErr    error
	statusResp *Status
	statusErr  error
}

func (m *mockClient) IsUp(ctx context.Context) (bool, error) {
	if m.isUpErr != nil {
		return false, m.isUpErr
	}
	return m.isUp, nil
}

func (m *mockClient) Up(ctx context.Context) error {
	m.upCalls++
	if m.upErr != nil {
		return m.upErr
	}
	m.isUp = true
	return nil
}

func (m *mockClient) Down(ctx context.Context) error {
	m.downCalls++
	if m.downErr != nil {
		return m.downErr
	}
	m.isUp = false
	return nil
}

func (m *mockClient) Status(ctx context.Context) (*Status, error) {
	if m.statusErr != nil {
		return nil, m.statusErr
	}
	if m.statusResp != nil {
		return m.statusResp, nil
	}
	return &Status{
		Installed:    true,
		IsUp:         m.isUp,
		BackendState: map[bool]string{true: "Running", false: "Stopped"}[m.isUp],
	}, nil
}

func TestManagerAutoManageDisabled(t *testing.T) {
	mock := &mockClient{isUp: false}
	mgr := NewManagerWithClient(mock)

	started, err := mgr.OnLaunch(context.Background(), false)
	if err != nil {
		t.Fatalf("unexpected launch error: %v", err)
	}
	if started {
		t.Errorf("expected started=false when autoManage is disabled")
	}
	if mock.upCalls != 0 {
		t.Errorf("expected 0 upCalls, got %d", mock.upCalls)
	}

	if err := mgr.OnQuit(context.Background(), false); err != nil {
		t.Fatalf("unexpected quit error: %v", err)
	}
	if mock.downCalls != 0 {
		t.Errorf("expected 0 downCalls, got %d", mock.downCalls)
	}
}

func TestManagerTailscaleAlreadyUp(t *testing.T) {
	// If Tailscale was already running on launch with this setting on,
	// it should leave it as is and NOT turn it off when quitting.
	mock := &mockClient{isUp: true}
	mgr := NewManagerWithClient(mock)

	started, err := mgr.OnLaunch(context.Background(), true)
	if err != nil {
		t.Fatalf("unexpected launch error: %v", err)
	}
	if started {
		t.Errorf("expected started=false because tailscale was already running")
	}
	if mock.upCalls != 0 {
		t.Errorf("expected 0 upCalls, got %d", mock.upCalls)
	}
	if mgr.StartedByWaker() {
		t.Errorf("expected StartedByWaker to be false")
	}
	if !mgr.WasUpOnLaunch() {
		t.Errorf("expected WasUpOnLaunch to be true")
	}

	// Quit waker
	if err := mgr.OnQuit(context.Background(), true); err != nil {
		t.Fatalf("unexpected quit error: %v", err)
	}
	if mock.downCalls != 0 {
		t.Errorf("expected 0 downCalls (should NOT turn off when quitting!), got %d", mock.downCalls)
	}
}

func TestManagerTailscaleDownOnLaunch(t *testing.T) {
	// If Tailscale is not up, it runs tailscale up before scanning for devices status,
	// and when quitting waker it puts tailscale down.
	mock := &mockClient{isUp: false}
	mgr := NewManagerWithClient(mock)

	started, err := mgr.OnLaunch(context.Background(), true)
	if err != nil {
		t.Fatalf("unexpected launch error: %v", err)
	}
	if !started {
		t.Errorf("expected started=true")
	}
	if mock.upCalls != 1 {
		t.Errorf("expected 1 upCall, got %d", mock.upCalls)
	}
	if !mgr.StartedByWaker() {
		t.Errorf("expected StartedByWaker to be true")
	}
	if mgr.WasUpOnLaunch() {
		t.Errorf("expected WasUpOnLaunch to be false")
	}

	// Quit waker
	if err := mgr.OnQuit(context.Background(), true); err != nil {
		t.Fatalf("unexpected quit error: %v", err)
	}
	if mock.downCalls != 1 {
		t.Errorf("expected 1 downCall on quit, got %d", mock.downCalls)
	}
	if mgr.StartedByWaker() {
		t.Errorf("expected StartedByWaker to reset to false after quit")
	}
}

func TestManagerTailscaleUpFailure(t *testing.T) {
	mock := &mockClient{isUp: false, upErr: errors.New("network error")}
	mgr := NewManagerWithClient(mock)

	started, err := mgr.OnLaunch(context.Background(), true)
	if err == nil {
		t.Fatalf("expected error from Up, got nil")
	}
	if started {
		t.Errorf("expected started=false on failure")
	}
	if mgr.StartedByWaker() {
		t.Errorf("expected StartedByWaker=false on failure")
	}
}
