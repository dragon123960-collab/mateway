package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"time"

	feishuchannel "github.com/dongping/mateway/internal/channels/feishu"
	"github.com/dongping/mateway/internal/config"
	agentharness "github.com/dongping/mateway/internal/harness"
	hostruntime "github.com/dongping/mateway/internal/runtime"
	"github.com/dongping/mateway/internal/skills"
	"github.com/dongping/mateway/internal/tools"
)

type Service struct {
	Config  config.Config
	Catalog *skills.Catalog
	Watcher *skills.Watcher
	Invoker hostruntime.Invoker
	Runner  *agentharness.Harness
	Tools   *tools.Registry
}

var gatewayListen = net.Listen

func (s Service) Run(ctx context.Context) error {
	_ = writeRuntimeStatus(s.Config)
	lockPath := filepath.Join(s.Config.App.Home, "gateway.lock")
	lock, err := acquireGatewayLock(lockPath)
	if err != nil {
		return err
	}
	defer lock.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/skills", func(w http.ResponseWriter, r *http.Request) {
		if s.Tools != nil {
			specs, err := s.Tools.Specs(r.Context(), tools.Scope{})
			if err == nil && len(specs) > 0 {
				_ = json.NewEncoder(w).Encode(specs)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(s.Catalog.Snapshot())
	})
	server := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", s.Config.Gateway.Host, s.Config.Gateway.Port),
		Handler:           mux,
		ReadHeaderTimeout: 3 * time.Second,
	}
	listener, err := gatewayListen("tcp", server.Addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	if s.Config.Channels.Feishu.Enabled {
		ws := &feishuchannel.Service{
			Config:  s.Config.Channels.Feishu,
			Home:    s.Config.App.Home,
			Catalog: s.Catalog,
			Invoker: s.Invoker,
			Runner:  s.Runner,
		}
		if err := ws.Start(ctx); err != nil {
			return err
		}
		if s.Runner != nil {
			s.Runner.RegisterChannelNotifier("feishu", ws)
			defer s.Runner.RegisterChannelNotifier("feishu", nil)
		}
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	}
}
