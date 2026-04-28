package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dongping/mateway/internal/config"
)

func TestAcquireGatewayLockRejectsSecondHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.lock")
	first, err := acquireGatewayLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	second, err := acquireGatewayLock(path)
	if second != nil {
		_ = second.Close()
		t.Fatal("expected second lock acquisition to fail")
	}
	if !errors.Is(err, ErrGatewayAlreadyRunning) {
		t.Fatalf("expected ErrGatewayAlreadyRunning, got %v", err)
	}
}

func TestServiceRunBindsBeforeFeishuStartup(t *testing.T) {
	origListen := gatewayListen
	t.Cleanup(func() { gatewayListen = origListen })

	blocked := make(chan struct{})
	gatewayListen = func(network, address string) (net.Listener, error) {
		close(blocked)
		return nil, errors.New("bind failed")
	}

	cfg := config.Default()
	cfg.App.Home = t.TempDir()
	cfg.Gateway.Host = "127.0.0.1"
	cfg.Gateway.Port = 8787
	cfg.Channels.Feishu.Enabled = true
	cfg.Channels.Feishu.AppID = "demo-app"
	cfg.Channels.Feishu.AppSecret = "demo-secret"

	err := Service{Config: cfg}.Run(context.Background())
	if err == nil || err.Error() != "bind failed" {
		t.Fatalf("expected bind failure, got %v", err)
	}
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("expected listener acquisition to run")
	}
}

func TestServiceRunWritesGatewayState(t *testing.T) {
	origListen := gatewayListen
	t.Cleanup(func() { gatewayListen = origListen })

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().(*net.TCPAddr)
	gatewayListen = func(network, address string) (net.Listener, error) {
		return listener, nil
	}

	cfg := config.Default()
	cfg.App.Home = t.TempDir()
	cfg.Gateway.Host = "127.0.0.1"
	cfg.Gateway.Port = addr.Port

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Service{Config: cfg}.Run(ctx)
	}()

	statePath := filepath.Join(cfg.App.Home, "gateway_state.json")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(statePath); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["gateway_host"] != "127.0.0.1" {
		t.Fatalf("unexpected state payload: %v", payload)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("service did not stop in time")
	}
}
