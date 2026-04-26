package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

func (s Service) Run(ctx context.Context) error {
	_ = writeRuntimeStatus(s.Config)
	if s.Config.Channels.Feishu.Enabled {
		ws := &feishuchannel.Service{
			Config:  s.Config.Channels.Feishu,
			Catalog: s.Catalog,
			Invoker: s.Invoker,
			Runner:  s.Runner,
		}
		if err := ws.Start(ctx); err != nil {
			return err
		}
	}
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
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe()
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
		return err
	}
}
