package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkdispatcher "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/dongping/mateway/internal/config"
	agentharness "github.com/dongping/mateway/internal/harness"
)

type Service struct {
	Config  config.FeishuConfig
	Catalog SkillCatalog
	Invoker SkillInvoker
	Runner  *agentharness.Harness

	HTTPClient *http.Client

	client   *lark.Client
	wsClient *larkws.Client

	botOpenID atomic.Value
	seen      sync.Map
}

func (s *Service) Start(ctx context.Context) error {
	if !s.Config.Enabled {
		return nil
	}
	if strings.TrimSpace(s.Config.AppID) == "" || strings.TrimSpace(s.Config.AppSecret) == "" {
		return fmt.Errorf("feishu enabled but app_id/app_secret missing")
	}
	opts := []lark.ClientOptionFunc{}
	if s.Config.IsLark {
		opts = append(opts, lark.WithOpenBaseUrl(lark.LarkBaseUrl))
	}
	if s.HTTPClient != nil {
		opts = append(opts, lark.WithHttpClient(s.HTTPClient))
	}
	s.client = lark.NewClient(s.Config.AppID, s.Config.AppSecret, opts...)
	_ = s.fetchBotOpenID(ctx)

	dispatcher := larkdispatcher.NewEventDispatcher(s.Config.VerificationToken, s.Config.EncryptKey).
		OnP2ChatAccessEventBotP2pChatEnteredV1(func(context.Context, *larkim.P2ChatAccessEventBotP2pChatEnteredV1) error { return nil }).
		OnP2MessageReadV1(func(context.Context, *larkim.P2MessageReadV1) error { return nil }).
		OnP2MessageReceiveV1(s.handleMessageReceive)

	domain := lark.FeishuBaseUrl
	if s.Config.IsLark {
		domain = lark.LarkBaseUrl
	}
	s.wsClient = larkws.NewClient(
		s.Config.AppID,
		s.Config.AppSecret,
		larkws.WithEventHandler(dispatcher),
		larkws.WithDomain(domain),
	)
	go func() {
		_ = s.wsClient.Start(ctx)
	}()
	return nil
}

func (s *Service) handleMessageReceive(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		return nil
	}
	msg := event.Event.Message
	messageID := stringValue(msg.MessageId)
	chatID := stringValue(msg.ChatId)
	if chatID == "" {
		return nil
	}
	senderID := extractFeishuSenderID(event.Event.Sender)
	if s.isDuplicateMessage(messageID) {
		return nil
	}
	if s.isSelfMessage(senderID, event.Event.Sender) {
		return nil
	}
	if senderID != "" && len(s.Config.AllowFrom) > 0 && !contains(s.Config.AllowFrom, senderID) {
		return nil
	}

	chatType := stringValue(msg.ChatType)
	content := extractText(stringValue(msg.Content))
	if chatType != "p2p" {
		if s.Config.GroupTrigger.MentionOnly && !s.isBotMentioned(msg) {
			return nil
		}
		content = stripMentionPlaceholders(content, msg.Mentions)
	}
	content = strings.TrimSpace(content)
	if content == "" || stringValue(msg.MessageType) != larkim.MsgTypeText {
		return nil
	}

	sessionKey := deriveSessionKey("feishu", chatType, chatID, senderID)
	pendingReactionID := ""
	if s.ackReactionEnabled() {
		pendingReactionID = s.addReactionBestEffort(ctx, messageID, []string{"EYES", "SMILE"})
	}
	if s.ackTextEnabled() {
		_ = s.sendText(ctx, chatID, "👀 已收到，处理中...")
	}
	go s.processMessage(context.Background(), sessionKey, chatID, messageID, senderID, content, pendingReactionID)
	return nil
}

func (s *Service) isSelfMessage(senderID string, sender *larkim.EventSender) bool {
	if sender != nil && sender.SenderType != nil {
		if strings.EqualFold(*sender.SenderType, "app") || strings.EqualFold(*sender.SenderType, "bot") {
			return true
		}
	}
	known, _ := s.botOpenID.Load().(string)
	return known != "" && senderID != "" && senderID == known
}

func (s *Service) isDuplicateMessage(messageID string) bool {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return false
	}
	if _, loaded := s.seen.LoadOrStore(messageID, time.Now()); loaded {
		return true
	}
	go func(id string) {
		time.Sleep(10 * time.Minute)
		s.seen.Delete(id)
	}(messageID)
	return false
}

func (s *Service) sendText(ctx context.Context, chatID, text string) error {
	if s.client == nil {
		return fmt.Errorf("feishu client unavailable")
	}
	content, _ := json.Marshal(map[string]string{"text": text})
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.ReceiveIdTypeChatId).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType(larkim.MsgTypeText).
			Content(string(content)).
			Build()).
		Build()
	resp, err := s.client.Im.V1.Message.Create(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("feishu message create failed code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func (s *Service) processMessage(ctx context.Context, sessionKey, chatID, messageID, senderID, content, pendingReactionID string) {
	reply := s.buildReply(ctx, sessionKey, chatID, senderID, content)
	if strings.TrimSpace(reply) != "" {
		_ = s.sendText(ctx, chatID, reply)
	}
	if pendingReactionID != "" {
		_ = s.deleteReaction(ctx, messageID, pendingReactionID)
	}
	if strings.TrimSpace(reply) != "" {
		_ = s.addReactionBestEffort(ctx, messageID, []string{"DONE", "OK"})
	}
}

func (s *Service) buildReply(ctx context.Context, sessionKey, chatID, senderID, content string) string {
	if isSlashCommand(content) {
		return Handler{
			Config:  s.Config,
			Catalog: s.Catalog,
			Invoker: s.Invoker,
			Harness: s.Runner,
		}.handleText(ctx, sessionKey, content)
	}
	if s.Runner != nil {
		run, err := s.Runner.Start(ctx, agentharness.Request{
			SessionKey: sessionKey,
			ThreadID:   chatID,
			UserID:     senderID,
			Channel:    "feishu",
			UserText:   content,
			Mode:       "chat",
		}, nil)
		if err != nil {
			return formatRuntimeError(err)
		}
		return run.Result
	}
	return Handler{
		Config:  s.Config,
		Catalog: s.Catalog,
		Invoker: s.Invoker,
	}.handleText(ctx, sessionKey, content)
}

func (s *Service) ackTextEnabled() bool {
	return s.Config.AckTextEnabled == nil || *s.Config.AckTextEnabled
}

func (s *Service) ackReactionEnabled() bool {
	return s.Config.AckReactionEnabled == nil || *s.Config.AckReactionEnabled
}

func (s *Service) addReactionBestEffort(ctx context.Context, messageID string, emojiTypes []string) string {
	if s.client == nil || strings.TrimSpace(messageID) == "" {
		return ""
	}
	for _, emojiType := range emojiTypes {
		resp, err := s.client.Im.V1.MessageReaction.Create(ctx, larkim.NewCreateMessageReactionReqBuilder().
			MessageId(messageID).
			Body(larkim.NewCreateMessageReactionReqBodyBuilder().
				ReactionType(larkim.NewEmojiBuilder().EmojiType(emojiType).Build()).
				Build()).
			Build())
		if err != nil || resp == nil || !resp.Success() || resp.Data == nil || resp.Data.ReactionId == nil {
			continue
		}
		return *resp.Data.ReactionId
	}
	return ""
}

func (s *Service) deleteReaction(ctx context.Context, messageID, reactionID string) error {
	if s.client == nil || strings.TrimSpace(messageID) == "" || strings.TrimSpace(reactionID) == "" {
		return nil
	}
	resp, err := s.client.Im.V1.MessageReaction.Delete(ctx, larkim.NewDeleteMessageReactionReqBuilder().
		MessageId(messageID).
		ReactionId(reactionID).
		Build())
	if err != nil {
		return err
	}
	if resp == nil || !resp.Success() {
		return fmt.Errorf("feishu reaction delete failed")
	}
	return nil
}

func (s *Service) fetchBotOpenID(ctx context.Context) error {
	if s.client == nil {
		return fmt.Errorf("feishu client unavailable")
	}
	resp, err := s.client.Do(ctx, &larkcore.ApiReq{
		HttpMethod:                "GET",
		ApiPath:                   "/open-apis/bot/v3/info",
		SupportedAccessTokenTypes: []larkcore.AccessTokenType{larkcore.AccessTokenTypeTenant},
	})
	if err != nil {
		return err
	}
	var result struct {
		Code int `json:"code"`
		Bot  struct {
			OpenID string `json:"open_id"`
		} `json:"bot"`
	}
	if err := json.Unmarshal(resp.RawBody, &result); err != nil {
		return err
	}
	if result.Code != 0 || result.Bot.OpenID == "" {
		return fmt.Errorf("bot info unavailable")
	}
	s.botOpenID.Store(result.Bot.OpenID)
	return nil
}

func (s *Service) isBotMentioned(message *larkim.EventMessage) bool {
	known, _ := s.botOpenID.Load().(string)
	if known == "" || message == nil || len(message.Mentions) == 0 {
		return false
	}
	for _, mention := range message.Mentions {
		if mention != nil && mention.Id != nil && mention.Id.OpenId != nil && *mention.Id.OpenId == known {
			return true
		}
	}
	return false
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func extractFeishuSenderID(sender *larkim.EventSender) string {
	if sender == nil || sender.SenderId == nil {
		return ""
	}
	if sender.SenderId.OpenId != nil && *sender.SenderId.OpenId != "" {
		return *sender.SenderId.OpenId
	}
	if sender.SenderId.UserId != nil && *sender.SenderId.UserId != "" {
		return *sender.SenderId.UserId
	}
	if sender.SenderId.UnionId != nil && *sender.SenderId.UnionId != "" {
		return *sender.SenderId.UnionId
	}
	return ""
}

func stripMentionPlaceholders(content string, mentions []*larkim.MentionEvent) string {
	for _, m := range mentions {
		if m != nil && m.Key != nil && *m.Key != "" {
			content = strings.ReplaceAll(content, *m.Key, "")
		}
	}
	return strings.TrimSpace(content)
}

func isSlashCommand(text string) bool {
	text = strings.TrimSpace(text)
	return text == "/skills" || strings.EqualFold(text, "skills") || text == "/tools" || text == "/runs" || text == "/summary" || text == "/last" || text == "/trace" || text == "/learn" || strings.HasPrefix(text, "/trace ") || strings.HasPrefix(text, "/learn ") || strings.HasPrefix(text, "/memory ") || strings.HasPrefix(text, "/run ") || strings.HasPrefix(text, "/run_status ") || strings.HasPrefix(text, "/agent ") || text == "/approve" || strings.HasPrefix(text, "/approve ") || text == "/deny" || strings.HasPrefix(text, "/deny ") || text == "/approvals"
}

func deriveSessionKey(platform, chatType, chatID, senderID string) string {
	platform = strings.TrimSpace(platform)
	chatType = strings.TrimSpace(chatType)
	chatID = strings.TrimSpace(chatID)
	senderID = strings.TrimSpace(senderID)
	if chatType == "p2p" && senderID != "" {
		return fmt.Sprintf("%s:p2p:%s", platform, senderID)
	}
	if chatID != "" {
		return fmt.Sprintf("%s:%s:%s", platform, firstNonEmpty(chatType, "chat"), chatID)
	}
	return fmt.Sprintf("%s:%s", platform, firstNonEmpty(senderID, "unknown"))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
