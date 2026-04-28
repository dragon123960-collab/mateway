package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkdispatcher "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	"golang.org/x/sys/unix"

	"github.com/dongping/mateway/internal/config"
	agentharness "github.com/dongping/mateway/internal/harness"
)

type Service struct {
	Config  config.FeishuConfig
	Home    string
	Catalog SkillCatalog
	Invoker SkillInvoker
	Runner  *agentharness.Harness

	HTTPClient *http.Client

	client   *lark.Client
	wsClient *larkws.Client

	botOpenID atomic.Value
	seen      sync.Map

	queueMu           sync.Mutex
	sessionQueues     map[string][]queuedMessage
	sessionProcessing map[string]bool
	threadStates      map[string]*threadTaskState
}

const feishuSeenTTL = 10 * time.Minute

type persistedSeen map[string]int64

type queuedMessage struct {
	sessionKey        string
	chatID            string
	messageID         string
	senderID          string
	content           string
	pendingReactionID string
	kind              queuedMessageKind
	taskID            string
}

type queuedMessageKind string

const (
	queuedMessageControl      queuedMessageKind = "control"
	queuedMessageApproval     queuedMessageKind = "approval"
	queuedMessageFollowUp     queuedMessageKind = "follow_up"
	queuedMessageNewTask      queuedMessageKind = "new_task"
	threadTaskContinuationTTL                   = 30 * time.Minute
)

func (k queuedMessageKind) priority() int {
	switch k {
	case queuedMessageApproval, queuedMessageControl:
		return 0
	default:
		return 1
	}
}

type threadTaskState struct {
	LastTaskSeq       int
	ActiveTaskID      string
	ActiveTaskStarted time.Time
	LastTaskID        string
	LastTaskUpdatedAt time.Time
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
		log.Printf("feishu duplicate message skipped message_id=%s chat_id=%s sender_id=%s", messageID, chatID, senderID)
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

	sessionKey := s.deriveSessionKey("feishu", chatType, chatID, senderID)
	log.Printf("feishu message accepted message_id=%s chat_id=%s sender_id=%s session_key=%s text=%q", messageID, chatID, senderID, sessionKey, trimForLog(content, 120))
	pendingReactionID := ""
	if s.ackReactionEnabled() {
		pendingReactionID = s.addReactionBestEffort(ctx, messageID, []string{"EYES", "SMILE"})
	}
	if s.ackTextEnabled() {
		_ = s.sendText(ctx, chatID, "👀 已收到，处理中...")
	}
	kind, taskID := s.classifyQueuedMessage(sessionKey, content)
	queued := queuedMessage{
		sessionKey:        sessionKey,
		chatID:            chatID,
		messageID:         messageID,
		senderID:          senderID,
		content:           content,
		pendingReactionID: pendingReactionID,
		kind:              kind,
		taskID:            taskID,
	}
	if s.enqueueSessionMessage(queued) {
		go s.processSessionQueue(context.Background(), sessionKey)
	}
	return nil
}

func (s *Service) enqueueSessionMessage(item queuedMessage) bool {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	if s.sessionQueues == nil {
		s.sessionQueues = make(map[string][]queuedMessage)
	}
	if s.sessionProcessing == nil {
		s.sessionProcessing = make(map[string]bool)
	}
	if s.threadStates == nil {
		s.threadStates = make(map[string]*threadTaskState)
	}
	queue := append([]queuedMessage(nil), s.sessionQueues[item.sessionKey]...)
	insertAt := len(queue)
	for i, queued := range queue {
		if item.kind.priority() < queued.kind.priority() {
			insertAt = i
			break
		}
	}
	queue = append(queue, queuedMessage{})
	copy(queue[insertAt+1:], queue[insertAt:])
	queue[insertAt] = item
	s.sessionQueues[item.sessionKey] = queue
	if s.sessionProcessing[item.sessionKey] {
		return false
	}
	s.sessionProcessing[item.sessionKey] = true
	return true
}

func (s *Service) popSessionMessage(sessionKey string) (queuedMessage, bool) {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	queue := s.sessionQueues[sessionKey]
	if len(queue) == 0 {
		delete(s.sessionQueues, sessionKey)
		delete(s.sessionProcessing, sessionKey)
		return queuedMessage{}, false
	}
	item := queue[0]
	if len(queue) == 1 {
		delete(s.sessionQueues, sessionKey)
	} else {
		s.sessionQueues[sessionKey] = queue[1:]
	}
	return item, true
}

func (s *Service) processSessionQueue(ctx context.Context, sessionKey string) {
	for {
		item, ok := s.popSessionMessage(sessionKey)
		if !ok {
			return
		}
		s.markQueuedMessageStart(item)
		s.processMessage(ctx, item)
		s.markQueuedMessageDone(item)
	}
}

func (s *Service) classifyQueuedMessage(sessionKey, content string) (queuedMessageKind, string) {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	state := s.threadStateLocked(sessionKey)
	content = strings.TrimSpace(content)
	if isSlashCommand(content) {
		return queuedMessageControl, ""
	}
	if hasApprovalIntent(content) && s.hasPendingApprovalLocked(sessionKey) {
		return queuedMessageApproval, state.ActiveTaskID
	}
	if shouldContinueTask(content, state) {
		return queuedMessageFollowUp, firstNonEmpty(state.ActiveTaskID, state.LastTaskID)
	}
	state.LastTaskSeq++
	taskID := fmt.Sprintf("task_%s_%d", sanitizeTaskToken(sessionKey), state.LastTaskSeq)
	return queuedMessageNewTask, taskID
}

func (s *Service) deriveSessionKey(platform, chatType, chatID, senderID string) string {
	platform = strings.TrimSpace(platform)
	chatType = strings.TrimSpace(chatType)
	chatID = strings.TrimSpace(chatID)
	senderID = strings.TrimSpace(senderID)
	if chatType == "p2p" && senderID != "" {
		return fmt.Sprintf("%s:p2p:%s", platform, senderID)
	}
	if chatID != "" {
		if strings.EqualFold(strings.TrimSpace(s.Config.GroupTrigger.SessionMode), "per_user") && senderID != "" {
			return fmt.Sprintf("%s:%s:%s:%s", platform, firstNonEmpty(chatType, "chat"), chatID, senderID)
		}
		return fmt.Sprintf("%s:%s:%s", platform, firstNonEmpty(chatType, "chat"), chatID)
	}
	return fmt.Sprintf("%s:%s", platform, firstNonEmpty(senderID, "unknown"))
}

func (s *Service) threadStateLocked(sessionKey string) *threadTaskState {
	if s.threadStates == nil {
		s.threadStates = make(map[string]*threadTaskState)
	}
	key := strings.TrimSpace(sessionKey)
	state, ok := s.threadStates[key]
	if !ok {
		state = &threadTaskState{}
		s.threadStates[key] = state
	}
	return state
}

func (s *Service) hasPendingApprovalLocked(sessionKey string) bool {
	if s.Runner == nil {
		return false
	}
	return len(s.Runner.ListPending(strings.TrimSpace(sessionKey))) > 0
}

func (s *Service) markQueuedMessageStart(item queuedMessage) {
	if strings.TrimSpace(item.taskID) == "" {
		return
	}
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	state := s.threadStateLocked(item.sessionKey)
	state.ActiveTaskID = item.taskID
	state.ActiveTaskStarted = time.Now()
}

func (s *Service) markQueuedMessageDone(item queuedMessage) {
	if strings.TrimSpace(item.taskID) == "" {
		return
	}
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	state := s.threadStateLocked(item.sessionKey)
	state.LastTaskID = item.taskID
	state.LastTaskUpdatedAt = time.Now()
	if state.ActiveTaskID == item.taskID {
		state.ActiveTaskID = ""
		state.ActiveTaskStarted = time.Time{}
	}
}

func shouldContinueTask(content string, state *threadTaskState) bool {
	if state == nil {
		return false
	}
	targetTaskID := strings.TrimSpace(firstNonEmpty(state.ActiveTaskID, state.LastTaskID))
	if targetTaskID == "" {
		return false
	}
	if state.ActiveTaskID == "" && (state.LastTaskUpdatedAt.IsZero() || time.Since(state.LastTaskUpdatedAt) > threadTaskContinuationTTL) {
		return false
	}
	if looksLikeFreshRequest(content) && !looksLikeContinuation(content) {
		return false
	}
	if looksLikeContinuation(content) {
		return true
	}
	return looksLikeShortClarification(content)
}

func looksLikeFreshRequest(content string) bool {
	if looksLikeExplicitTimeCue(content) {
		return true
	}
	return containsAnyLower(content,
		"请帮我", "创建", "查一下", "整理", "生成", "写一个", "分析", "总结", "提醒", "每天", "明天",
		"please", "create", "check", "summarize", "analyze", "generate",
	)
}

func looksLikeExplicitTimeCue(content string) bool {
	content = strings.TrimSpace(strings.ToLower(content))
	if content == "" {
		return false
	}
	timeHints := []string{
		"今天", "明天", "后天", "下周", "下个月", "本周", "今晚", "上午", "下午", "晚上",
		"today", "tomorrow", "next week", "next month", "tonight",
	}
	for _, hint := range timeHints {
		if strings.Contains(content, hint) {
			return true
		}
	}
	if strings.ContainsAny(content, "0123456789") {
		for _, token := range []string{"年", "月", "日", "号", "点", "时", "分", "-", "/", "am", "pm"} {
			if strings.Contains(content, token) {
				return true
			}
		}
	}
	return false
}

func looksLikeContinuation(content string) bool {
	return containsAnyLower(content,
		"继续", "刚才", "这个", "那个", "上一个", "补充", "改一下", "再来一版", "基于刚才", "就按这个", "展开",
		"continue", "follow up", "based on that", "expand", "revise", "update this",
	)
}

func looksLikeShortClarification(content string) bool {
	content = strings.TrimSpace(content)
	if content == "" {
		return false
	}
	if len([]rune(content)) > 24 {
		return false
	}
	return !looksLikeFreshRequest(content)
}

func hasApprovalIntent(content string) bool {
	_, ok := detectApprovalIntent(content)
	return ok
}

func containsAnyLower(text string, items ...string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	for _, item := range items {
		if strings.Contains(text, strings.ToLower(strings.TrimSpace(item))) {
			return true
		}
	}
	return false
}

func sanitizeTaskToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, ":", "_")
	value = strings.ReplaceAll(value, "/", "_")
	value = strings.ReplaceAll(value, " ", "_")
	value = strings.Trim(value, "_")
	if value == "" {
		return "thread"
	}
	return value
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
	duplicated, err := s.rememberMessageID(messageID, time.Now())
	if err == nil && duplicated {
		return true
	}
	go func(id string) {
		time.Sleep(feishuSeenTTL)
		s.seen.Delete(id)
	}(messageID)
	return false
}

func (s *Service) rememberMessageID(messageID string, now time.Time) (bool, error) {
	path := s.seenStorePath()
	if strings.TrimSpace(path) == "" {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return false, err
	}
	defer file.Close()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		return false, err
	}
	defer func() {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
	}()

	seen := persistedSeen{}
	if stat, err := file.Stat(); err == nil && stat.Size() > 0 {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			_ = json.Unmarshal(data, &seen)
		}
	}
	cutoff := now.Add(-feishuSeenTTL).Unix()
	for id, ts := range seen {
		if ts < cutoff {
			delete(seen, id)
		}
	}
	if ts, ok := seen[messageID]; ok && ts >= cutoff {
		return true, nil
	}
	seen[messageID] = now.Unix()
	data, err := json.Marshal(seen)
	if err != nil {
		return false, err
	}
	if err := file.Truncate(0); err != nil {
		return false, err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return false, err
	}
	if _, err := file.Write(data); err != nil {
		return false, err
	}
	return false, nil
}

func (s *Service) seenStorePath() string {
	home := strings.TrimSpace(s.Home)
	if home == "" {
		return ""
	}
	return filepath.Join(home, "feishu_seen_messages.json")
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

func (s *Service) processMessage(ctx context.Context, item queuedMessage) {
	log.Printf("feishu processing started message_id=%s session_key=%s sender_id=%s kind=%s task_id=%s", item.messageID, item.sessionKey, item.senderID, item.kind, item.taskID)
	reply := s.buildReplyWithRetry(ctx, item)
	if strings.TrimSpace(reply) == "" {
		switch item.kind {
		case queuedMessageApproval, queuedMessageFollowUp, queuedMessageNewTask:
			reply = "任务已完成，但这次没有返回可展示的文本结果。"
		}
	}
	if strings.TrimSpace(reply) != "" {
		_ = s.sendText(ctx, item.chatID, reply)
	}
	if item.pendingReactionID != "" {
		_ = s.deleteReaction(ctx, item.messageID, item.pendingReactionID)
	}
	if strings.TrimSpace(reply) != "" {
		_ = s.addReactionBestEffort(ctx, item.messageID, []string{"DONE", "OK"})
	}
	log.Printf("feishu processing finished message_id=%s session_key=%s replied=%t", item.messageID, item.sessionKey, strings.TrimSpace(reply) != "")
}

func (s *Service) buildReplyWithRetry(ctx context.Context, item queuedMessage) string {
	const maxBusyRetries = 20
	for attempt := 0; attempt < maxBusyRetries; attempt++ {
		reply := s.buildReply(ctx, item)
		if !looksLikeBusyReply(reply) {
			return reply
		}
		time.Sleep(300 * time.Millisecond)
	}
	return busyFallbackReply(item)
}

func busyFallbackReply(item queuedMessage) string {
	switch item.kind {
	case queuedMessageControl, queuedMessageApproval:
		return "前一个任务还没有完全释放执行状态，这次控制消息没有丢失，但暂时没能立即执行。请稍后重试，或先发 `/runs` 查看最近任务。"
	default:
		return "当前线程前一个任务仍占用执行状态，这次请求没有丢失，但暂时没能继续执行。请稍后重试，或先发 `/runs` `/trace` 查看最近任务。"
	}
}

func (s *Service) buildReply(ctx context.Context, item queuedMessage) string {
	if isSlashCommand(item.content) {
		log.Printf("feishu command dispatch session_key=%s sender_id=%s command=%q", item.sessionKey, item.senderID, trimForLog(item.content, 120))
	} else {
		log.Printf("feishu chat dispatch session_key=%s sender_id=%s kind=%s task_id=%s text=%q", item.sessionKey, item.senderID, item.kind, item.taskID, trimForLog(item.content, 120))
	}
	return Handler{
		Config:   s.Config,
		Catalog:  s.Catalog,
		Invoker:  s.Invoker,
		Harness:  s.Runner,
		ThreadID: item.chatID,
		UserID:   item.senderID,
		TaskID:   item.taskID,
		TaskKind: string(item.kind),
	}.handleText(ctx, item.sessionKey, item.content)
}

func looksLikeBusyReply(reply string) bool {
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return false
	}
	return strings.Contains(reply, "上一条消息还在处理中") || strings.Contains(reply, "当前 session 正在处理中")
}

func (s *Service) NotifyAsyncResult(ctx context.Context, event agentharness.AsyncResultEvent) error {
	if !strings.EqualFold(strings.TrimSpace(event.Channel), "feishu") {
		return nil
	}
	chatID := strings.TrimSpace(event.ThreadID)
	if chatID == "" {
		return fmt.Errorf("feishu async result missing chat id")
	}
	return s.sendText(ctx, chatID, formatAsyncResultMessage(event))
}

func formatAsyncResultMessage(event agentharness.AsyncResultEvent) string {
	title := "异步任务已完成"
	if strings.EqualFold(event.Status, "failed") {
		title = "异步任务执行失败"
	}
	lines := []string{title}
	if goal := strings.TrimSpace(event.Goal); goal != "" {
		lines = append(lines, "任务: "+trimBlock(goal))
	}
	if runID := strings.TrimSpace(event.RunID); runID != "" {
		lines = append(lines, "run: "+runID)
	}
	if strings.EqualFold(event.Status, "failed") {
		errText := strings.TrimSpace(event.Error)
		if errText == "" {
			errText = "unknown error"
		}
		lines = append(lines, "错误: "+trimBlock(errText))
		return strings.Join(lines, "\n")
	}
	result := strings.TrimSpace(event.Result)
	if result == "" {
		result = "(结果为空)"
	}
	lines = append(lines, "结果:\n"+trimBlock(result))
	return strings.Join(lines, "\n")
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
	return text == "/new" || text == "/skills" || strings.EqualFold(text, "skills") || text == "/tools" || text == "/runs" || text == "/summary" || text == "/last" || text == "/trace" || text == "/learn" || strings.HasPrefix(text, "/trace ") || strings.HasPrefix(text, "/learn ") || strings.HasPrefix(text, "/memory ") || strings.HasPrefix(text, "/run ") || strings.HasPrefix(text, "/run_status ") || strings.HasPrefix(text, "/agent ") || text == "/approve" || strings.HasPrefix(text, "/approve ") || text == "/deny" || strings.HasPrefix(text, "/deny ") || text == "/approvals" || text == "/schedule" || strings.HasPrefix(text, "/schedule ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func trimForLog(value string, max int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if max > 0 && len(value) > max {
		return value[:max] + "..."
	}
	return value
}
