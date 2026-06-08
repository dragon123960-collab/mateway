package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/channel/feishu"
	"github.com/dongping/mateway/internal/channel/weixin"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/session"
)

type FetchHistoryOptions struct {
	Config     *config.Root
	From       string
	SessionKey string
	Limit      int
	Since      time.Duration
	Out        io.Writer
}

type FetchHistoryResult struct {
	Channel    string
	SessionKey string
	Imported   int
	Skipped    int
}

func RunFetchHistory(ctx context.Context, opts FetchHistoryOptions) (FetchHistoryResult, error) {
	if opts.Config == nil {
		return FetchHistoryResult{}, fmt.Errorf("config is required")
	}
	target, err := parseHistoryTarget(opts.From)
	if err != nil {
		return FetchHistoryResult{}, err
	}
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	switch target.Channel {
	case "feishu":
		return fetchFeishuHistory(ctx, opts, target)
	case "weixin":
		return fetchWeixinHistory(ctx, opts, target)
	default:
		return FetchHistoryResult{}, fmt.Errorf("unsupported history channel %q", target.Channel)
	}
}

type historyTarget struct {
	Channel string
	Account string
	ID      string
}

func parseHistoryTarget(value string) (historyTarget, error) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, ":")
	if len(parts) < 2 || strings.TrimSpace(parts[0]) == "" {
		return historyTarget{}, fmt.Errorf("usage: --from <channel:target>")
	}
	target := historyTarget{Channel: strings.ToLower(strings.TrimSpace(parts[0]))}
	switch target.Channel {
	case "feishu", "weixin":
		if len(parts) == 2 {
			target.ID = strings.TrimSpace(parts[1])
		} else {
			target.Account = strings.TrimSpace(parts[1])
			target.ID = strings.TrimSpace(strings.Join(parts[2:], ":"))
		}
	default:
		return historyTarget{}, fmt.Errorf("unsupported history channel %q", target.Channel)
	}
	if target.ID == "" {
		return historyTarget{}, fmt.Errorf("history target id is required")
	}
	return target, nil
}

func fetchFeishuHistory(ctx context.Context, opts FetchHistoryOptions, target historyTarget) (FetchHistoryResult, error) {
	cfg, err := feishuConfigForTarget(opts.Config.Channels.Feishu, target.Account)
	if err != nil {
		return FetchHistoryResult{}, err
	}
	until := time.Now()
	since := time.Time{}
	if opts.Since > 0 {
		since = until.Add(-opts.Since)
	}
	items, err := feishu.NewSender(cfg).ListMessages(ctx, target.ID, opts.Limit, since, until)
	if err != nil {
		return FetchHistoryResult{}, err
	}
	key := firstNonEmpty(opts.SessionKey, "feishu:"+target.ID)
	messages := make([]agentcore.Message, 0, len(items))
	for _, item := range items {
		if msg, ok := agentMessageFromFeishu(item); ok {
			messages = append(messages, msg)
		}
	}
	result, err := importHistoryMessages(opts.Config, key, messages)
	if err != nil {
		return FetchHistoryResult{}, err
	}
	result.Channel = "feishu"
	return result, nil
}

func agentMessageFromFeishu(item *larkim.Message) (agentcore.Message, bool) {
	if item == nil || item.Deleted != nil && *item.Deleted {
		return agentcore.Message{}, false
	}
	text := feishuMessageText(item)
	if strings.TrimSpace(text) == "" {
		return agentcore.Message{}, false
	}
	senderType := ""
	senderID := ""
	if item.Sender != nil {
		senderType = ptrString(item.Sender.SenderType)
		senderID = firstNonEmpty(ptrString(item.Sender.Id), ptrString(item.Sender.IdType))
	}
	role := agentcore.RoleUser
	if strings.EqualFold(senderType, "app") {
		role = agentcore.RoleAssistant
	}
	prefix := "[feishu " + firstNonEmpty(senderType, "sender")
	if senderID != "" {
		prefix += ":" + senderID
	}
	prefix += "] "
	return agentcore.Message{Role: role, Content: prefix + text}, true
}

func feishuMessageText(item *larkim.Message) string {
	msgType := ptrString(item.MsgType)
	content := ""
	if item.Body != nil {
		content = ptrString(item.Body.Content)
	}
	if strings.EqualFold(msgType, "text") {
		var payload map[string]any
		if json.Unmarshal([]byte(content), &payload) == nil {
			return compactInline(fmt.Sprint(payload["text"]), 4000)
		}
		return compactInline(content, 4000)
	}
	if msgType != "" {
		return "[" + msgType + " message]"
	}
	return compactInline(content, 4000)
}

func fetchWeixinHistory(ctx context.Context, opts FetchHistoryOptions, target historyTarget) (FetchHistoryResult, error) {
	cfg := opts.Config.Channels.Weixin.ResolveSecrets()
	account, err := loadWeixinAccount(opts.Config.App.Home, cfg, target.Account)
	if err == nil {
		cfg.AccountID = firstNonEmpty(cfg.AccountID, account.AccountID)
		cfg.Token = firstNonEmpty(cfg.Token, account.Token)
		cfg.BaseURL = firstNonEmpty(cfg.BaseURL, account.BaseURL)
	} else if strings.TrimSpace(cfg.Token) == "" {
		return FetchHistoryResult{}, err
	}
	dir := strings.TrimSpace(cfg.AccountDir)
	if dir == "" {
		dir = filepath.Join(opts.Config.App.Home, "run", "weixin", "accounts")
	}
	syncBuf := readWeixinSyncBuf(dir, cfg.AccountID)
	resp, err := (weixin.Client{BaseURL: cfg.BaseURL, Token: cfg.Token}).GetUpdates(ctx, syncBuf, 5*time.Second)
	if err != nil {
		return FetchHistoryResult{}, err
	}
	writeWeixinSyncBuf(dir, cfg.AccountID, resp.GetUpdatesBuf)
	key := firstNonEmpty(opts.SessionKey, "weixin:"+firstNonEmpty(cfg.AccountID, "default")+":"+target.ID)
	var messages []agentcore.Message
	for _, item := range resp.Msgs {
		inbound, ok := item.ToInbound(cfg.AccountID)
		if !ok || firstNonEmpty(inbound.Metadata["peer_id"], inbound.UserID, inbound.ThreadID) != target.ID {
			continue
		}
		if strings.TrimSpace(inbound.Text) == "" {
			continue
		}
		messages = append(messages, agentcore.Message{Role: agentcore.RoleUser, Content: "[weixin user:" + firstNonEmpty(inbound.UserID, target.ID) + "] " + compactInline(inbound.Text, 4000)})
	}
	result, err := importHistoryMessages(opts.Config, key, messages)
	if err != nil {
		return FetchHistoryResult{}, err
	}
	result.Channel = "weixin"
	return result, nil
}

func importHistoryMessages(cfg *config.Root, sessionKey string, messages []agentcore.Message) (FetchHistoryResult, error) {
	store := session.NewStore(cfg.App.Home)
	state, err := store.Load(sessionKey)
	if err != nil {
		return FetchHistoryResult{}, err
	}
	existing := map[string]bool{}
	for _, msg := range state.Messages {
		existing[string(msg.Role)+"\x00"+msg.Content] = true
	}
	imported := 0
	skipped := 0
	for _, msg := range messages {
		key := string(msg.Role) + "\x00" + msg.Content
		if existing[key] {
			skipped++
			continue
		}
		existing[key] = true
		state.Messages = append(state.Messages, msg)
		imported++
	}
	state.Key = sessionKey
	if err := store.Save(state); err != nil {
		return FetchHistoryResult{}, err
	}
	return FetchHistoryResult{SessionKey: sessionKey, Imported: imported, Skipped: skipped}, nil
}

func PrintFetchHistoryResult(out io.Writer, result FetchHistoryResult) {
	if out == nil {
		return
	}
	fmt.Fprintln(out, "channel:", result.Channel)
	fmt.Fprintln(out, "session:", result.SessionKey)
	fmt.Fprintln(out, "imported:", result.Imported)
	fmt.Fprintln(out, "skipped:", result.Skipped)
}

func ptrString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func readWeixinSyncBuf(dir, accountID string) string {
	data, err := os.ReadFile(filepath.Join(dir, strings.TrimSpace(accountID)+".sync.json"))
	if err != nil {
		return ""
	}
	var payload struct {
		SyncBuf string `json:"sync_buf"`
	}
	_ = json.Unmarshal(data, &payload)
	return payload.SyncBuf
}

func writeWeixinSyncBuf(dir, accountID, syncBuf string) {
	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(syncBuf) == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o700)
	data, _ := json.MarshalIndent(map[string]string{"sync_buf": syncBuf}, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, strings.TrimSpace(accountID)+".sync.json"), data, 0o600)
}

func ParseSince(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 24 * time.Hour, nil
	}
	if duration, err := time.ParseDuration(value); err == nil {
		return duration, nil
	}
	days, err := strconv.Atoi(value)
	if err != nil {
		return 0, err
	}
	return time.Duration(days) * 24 * time.Hour, nil
}
