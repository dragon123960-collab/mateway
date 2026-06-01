package openclawcompat

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
)

type Receiver func(context.Context, channel.InboundMessage) (channel.OutboundMessage, error)

type Server struct {
	cfg     config.OpenClawCompatConfig
	receive Receiver
	mu      sync.Mutex
	nextSeq int64
	msgs    []WeixinMessage
}

type SendMessageRequest struct {
	Msg WeixinMessage `json:"msg"`
}

type GetUpdatesRequest struct {
	GetUpdatesBuf string `json:"get_updates_buf"`
}

type WeixinMessage struct {
	Seq          int64         `json:"seq,omitempty"`
	MessageID    int64         `json:"message_id,omitempty"`
	FromUserID   string        `json:"from_user_id,omitempty"`
	ToUserID     string        `json:"to_user_id,omitempty"`
	CreateTimeMS int64         `json:"create_time_ms,omitempty"`
	SessionID    string        `json:"session_id,omitempty"`
	MessageType  int           `json:"message_type,omitempty"`
	MessageState int           `json:"message_state,omitempty"`
	ItemList     []MessageItem `json:"item_list,omitempty"`
	ContextToken string        `json:"context_token,omitempty"`
	Unsupported  string        `json:"unsupported_reason,omitempty"`
	BotAgent     string        `json:"bot_agent,omitempty"`
}

type MessageItem struct {
	Type     int       `json:"type"`
	TextItem *TextItem `json:"text_item,omitempty"`
}

type TextItem struct {
	Text string `json:"text"`
}

func NewServer(cfg config.OpenClawCompatConfig, receiver Receiver) *Server {
	cfg = cfg.ResolveSecrets()
	if cfg.LongPollTimeoutMS <= 0 {
		cfg.LongPollTimeoutMS = 35000
	}
	if strings.TrimSpace(cfg.BotAgent) == "" {
		cfg.BotAgent = "Mateway/0.1"
	}
	return &Server{cfg: cfg, receive: receiver}
}

func Start(ctx context.Context, cfg config.OpenClawCompatConfig, receiver Receiver) error {
	cfg = cfg.ResolveSecrets()
	if !cfg.Enabled {
		return fmt.Errorf("openclaw compat channel is disabled")
	}
	addr := strings.TrimSpace(cfg.Addr)
	if addr == "" {
		addr = "127.0.0.1:8790"
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
	prefix := cleanPrefix(s.cfg.PathPrefix)
	mux.HandleFunc(prefix+"sendmessage", s.handleSendMessage)
	mux.HandleFunc(prefix+"getupdates", s.handleGetUpdates)
	mux.HandleFunc(prefix+"getconfig", s.handleGetConfig)
	mux.HandleFunc(prefix+"sendtyping", s.handleSendTyping)
	return mux
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	if !s.checkPOST(w, r) {
		return
	}
	var req SendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ret": 1, "errmsg": "invalid json"})
		return
	}
	text, ok := textFromItems(req.Msg.ItemList)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"ret": 2, "errmsg": "unsupported message item"})
		return
	}
	msg := normalizeInbound(req.Msg, text)
	go func() {
		reply, err := s.receive(context.Background(), msg)
		if err != nil {
			return
		}
		s.enqueueReply(req.Msg, reply)
	}()
	writeJSON(w, http.StatusOK, map[string]any{"ret": 0})
}

func (s *Server) handleGetUpdates(w http.ResponseWriter, r *http.Request) {
	if !s.checkPOST(w, r) {
		return
	}
	var req GetUpdatesRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	cursor, _ := strconv.ParseInt(strings.TrimSpace(req.GetUpdatesBuf), 10, 64)
	deadline := time.Now().Add(time.Duration(s.cfg.LongPollTimeoutMS) * time.Millisecond)
	for {
		msgs, next := s.messagesAfter(cursor)
		if len(msgs) > 0 || time.Now().After(deadline) {
			writeJSON(w, http.StatusOK, map[string]any{
				"ret":                    0,
				"msgs":                   msgs,
				"get_updates_buf":        strconv.FormatInt(next, 10),
				"longpolling_timeout_ms": s.cfg.LongPollTimeoutMS,
			})
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	if !s.checkPOST(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ret": 0, "typing_ticket": ""})
}

func (s *Server) handleSendTyping(w http.ResponseWriter, r *http.Request) {
	if !s.checkPOST(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ret": 0})
}

func (s *Server) checkPOST(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ret": 1, "errmsg": "method not allowed"})
		return false
	}
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ret": 1, "errmsg": "unauthorized"})
		return false
	}
	return true
}

func (s *Server) authorized(r *http.Request) bool {
	token := strings.TrimSpace(s.cfg.Token)
	if token == "" {
		return true
	}
	if strings.TrimSpace(r.Header.Get("AuthorizationType")) != "" && strings.TrimSpace(r.Header.Get("AuthorizationType")) != "ilink_bot_token" {
		return false
	}
	got := strings.TrimSpace(r.Header.Get("Authorization"))
	got = strings.TrimSpace(strings.TrimPrefix(got, "Bearer "))
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

func normalizeInbound(msg WeixinMessage, text string) channel.InboundMessage {
	id := strconv.FormatInt(msg.MessageID, 10)
	if msg.MessageID == 0 {
		id = strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	peerID := firstNonEmpty(msg.FromUserID, msg.ToUserID, msg.SessionID)
	in := channel.InboundMessage{
		ID:       id,
		Channel:  "openclaw-weixin",
		ThreadID: peerID,
		UserID:   firstNonEmpty(msg.FromUserID, peerID),
		Text:     strings.TrimSpace(text),
		Metadata: map[string]string{
			"account_id":      msg.ToUserID,
			"peer_id":         peerID,
			"session_id":      msg.SessionID,
			"context_token":   msg.ContextToken,
			"message_type":    strconv.Itoa(msg.MessageType),
			"message_state":   strconv.Itoa(msg.MessageState),
			"openclaw_compat": "true",
		},
	}
	in.SessionKey = "openclaw-weixin:" + firstNonEmpty(msg.ToUserID, "default") + ":" + firstNonEmpty(peerID, id)
	return in
}

func (s *Server) enqueueReply(original WeixinMessage, reply channel.OutboundMessage) {
	text := strings.TrimSpace(reply.Text)
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextSeq++
	messageID := time.Now().UnixNano()
	s.msgs = append(s.msgs, WeixinMessage{
		Seq:          s.nextSeq,
		MessageID:    messageID,
		FromUserID:   firstNonEmpty(original.ToUserID, "mateway"),
		ToUserID:     firstNonEmpty(original.FromUserID, original.ToUserID),
		CreateTimeMS: time.Now().UnixMilli(),
		SessionID:    original.SessionID,
		MessageType:  2,
		MessageState: 2,
		ContextToken: original.ContextToken,
		BotAgent:     s.cfg.BotAgent,
		ItemList: []MessageItem{{
			Type:     1,
			TextItem: &TextItem{Text: text},
		}},
	})
}

func (s *Server) messagesAfter(cursor int64) ([]WeixinMessage, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []WeixinMessage{}
	next := cursor
	for _, msg := range s.msgs {
		if msg.Seq > cursor {
			out = append(out, msg)
			if msg.Seq > next {
				next = msg.Seq
			}
		}
	}
	return out, next
}

func textFromItems(items []MessageItem) (string, bool) {
	for _, item := range items {
		if item.Type != 1 || item.TextItem == nil {
			continue
		}
		if text := strings.TrimSpace(item.TextItem.Text); text != "" {
			return text, true
		}
	}
	return "", false
}

func cleanPrefix(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "/"
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	if !strings.HasSuffix(value, "/") {
		value += "/"
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
