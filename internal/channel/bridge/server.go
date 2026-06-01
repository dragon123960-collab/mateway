package bridge

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
)

type Receiver func(context.Context, Event) (Reply, error)

type Server struct {
	cfg      config.BridgeConfig
	receive  Receiver
	client   *http.Client
	outbound map[string]string
	mu       sync.Mutex
	replies  []Reply
}

func NewServer(cfg config.BridgeConfig, receiver Receiver) *Server {
	cfg = cfg.ResolveSecrets()
	return &Server{
		cfg:      cfg,
		receive:  receiver,
		client:   &http.Client{Timeout: 15 * time.Second},
		outbound: map[string]string{},
	}
}

func Start(ctx context.Context, cfg config.BridgeConfig, receiver Receiver) error {
	cfg = cfg.ResolveSecrets()
	if !cfg.Enabled {
		return fmt.Errorf("bridge channel is disabled")
	}
	addr := strings.TrimSpace(cfg.Addr)
	if addr == "" {
		addr = "127.0.0.1:8789"
	}
	server := NewServer(cfg, receiver)
	httpServer := &http.Server{Addr: addr, Handler: server.Handler()}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	base := cleanBasePath(s.cfg.BasePath, "/channels")
	mux.HandleFunc(base+"/", s.handleChannels)
	return mux
}

func (s *Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	base := cleanBasePath(s.cfg.BasePath, "/channels")
	rel := strings.Trim(strings.TrimPrefix(r.URL.Path, base), "/")
	parts := strings.Split(rel, "/")
	if len(parts) != 2 {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
		return
	}
	channelName, action := parts[0], parts[1]
	switch action {
	case "health":
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": channelName})
	case "events":
		s.handleEvent(w, r, channelName)
	case "acks":
		s.handleAck(w, r, channelName)
	case "replies":
		s.handleReplies(w, r, channelName)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
	}
}

func (s *Server) handleEvent(w http.ResponseWriter, r *http.Request, channelName string) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "unauthorized"})
		return
	}
	if !s.channelAllowed(channelName) {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "channel not allowed"})
		return
	}
	var event Event
	if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid json"})
		return
	}
	if strings.TrimSpace(event.Channel) == "" {
		event.Channel = channelName
	}
	if err := validateEvent(event); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if outboundURL := strings.TrimSpace(r.Header.Get("X-Mateway-Outbound-URL")); outboundURL != "" {
		s.mu.Lock()
		s.outbound[event.Channel] = outboundURL
		s.mu.Unlock()
	}
	go func() {
		reply, err := s.receive(context.Background(), event)
		if err != nil {
			log.Printf("mateway bridge receive error channel=%s id=%s: %v", event.Channel, event.ID, err)
			return
		}
		s.deliverReply(context.Background(), event.Channel, reply)
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "id": event.ID, "status": "accepted"})
}

func (s *Server) handleAck(w http.ResponseWriter, r *http.Request, channelName string) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "unauthorized"})
		return
	}
	var ack Ack
	if err := json.NewDecoder(r.Body).Decode(&ack); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid json"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": channelName})
}

func (s *Server) handleReplies(w http.ResponseWriter, r *http.Request, channelName string) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "unauthorized"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	replies := s.replies[:0]
	out := []Reply{}
	for _, reply := range s.replies {
		if strings.EqualFold(reply.Channel, channelName) {
			out = append(out, reply)
			continue
		}
		replies = append(replies, reply)
	}
	s.replies = replies
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "replies": out})
}

func (s *Server) deliverReply(ctx context.Context, channelName string, reply Reply) {
	s.mu.Lock()
	outboundURL := s.outbound[channelName]
	if outboundURL == "" {
		s.replies = append(s.replies, reply)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	payload, _ := json.Marshal(reply)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, outboundURL, bytes.NewReader(payload))
	if err != nil {
		log.Printf("mateway bridge outbound request error channel=%s: %v", channelName, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if token := strings.TrimSpace(s.cfg.Token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		log.Printf("mateway bridge outbound error channel=%s: %v", channelName, err)
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("mateway bridge outbound non-2xx channel=%s status=%d", channelName, resp.StatusCode)
	}
}

func (s *Server) authorized(r *http.Request) bool {
	token := strings.TrimSpace(s.cfg.Token)
	if token == "" {
		return true
	}
	got := strings.TrimSpace(r.Header.Get("Authorization"))
	got = strings.TrimSpace(strings.TrimPrefix(got, "Bearer "))
	if got == "" {
		got = strings.TrimSpace(r.Header.Get("X-Mateway-Token"))
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

func (s *Server) channelAllowed(channelName string) bool {
	if len(s.cfg.AllowedChannels) == 0 {
		return true
	}
	for _, allowed := range s.cfg.AllowedChannels {
		if strings.EqualFold(strings.TrimSpace(allowed), strings.TrimSpace(channelName)) {
			return true
		}
	}
	return false
}

func validateEvent(event Event) error {
	if strings.TrimSpace(event.ID) == "" {
		return fmt.Errorf("id is required")
	}
	if strings.TrimSpace(event.Channel) == "" {
		return fmt.Errorf("channel is required")
	}
	if strings.TrimSpace(event.Text) == "" {
		return fmt.Errorf("text is required")
	}
	if len(event.Attachments) > 0 {
		return fmt.Errorf("attachments are not supported in bridge v1")
	}
	return nil
}

func cleanBasePath(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return strings.TrimRight(value, "/")
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func OutboundToReply(original Event, outbound channel.OutboundMessage) Reply {
	return ReplyFromOutbound(original, outbound)
}
