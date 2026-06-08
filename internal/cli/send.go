package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/channel/feishu"
	"github.com/dongping/mateway/internal/channel/weixin"
	"github.com/dongping/mateway/internal/config"
)

type SendOptions struct {
	Config *config.Root
	To     string
	Text   string
	UUID   string
	Out    io.Writer
}

func RunSend(ctx context.Context, opts SendOptions) error {
	if opts.Config == nil {
		return fmt.Errorf("config is required")
	}
	target, err := parseSendTarget(opts.To)
	if err != nil {
		return err
	}
	text := strings.TrimSpace(opts.Text)
	if text == "" {
		return fmt.Errorf("message text is required")
	}
	switch target.Channel {
	case "feishu":
		return sendFeishu(ctx, opts, target, text)
	case "weixin":
		return sendWeixin(ctx, opts, target, text)
	default:
		return fmt.Errorf("unsupported send channel %q", target.Channel)
	}
}

type sendTarget struct {
	Channel string
	Kind    string
	ID      string
	Account string
}

func parseSendTarget(value string) (sendTarget, error) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, ":")
	if len(parts) < 2 || strings.TrimSpace(parts[0]) == "" {
		return sendTarget{}, fmt.Errorf("usage: --to <channel:target>")
	}
	target := sendTarget{Channel: strings.ToLower(strings.TrimSpace(parts[0]))}
	switch target.Channel {
	case "feishu":
		target.Kind = "chat_id"
		switch len(parts) {
		case 2:
			target.ID = strings.TrimSpace(parts[1])
		case 3:
			target.Kind = strings.TrimSpace(parts[1])
			target.ID = strings.TrimSpace(parts[2])
		default:
			target.Account = strings.TrimSpace(parts[1])
			target.Kind = strings.TrimSpace(parts[2])
			target.ID = strings.TrimSpace(strings.Join(parts[3:], ":"))
		}
	case "weixin":
		if len(parts) == 2 {
			target.ID = strings.TrimSpace(parts[1])
		} else {
			target.Account = strings.TrimSpace(parts[1])
			target.ID = strings.TrimSpace(strings.Join(parts[2:], ":"))
		}
	default:
		return sendTarget{}, fmt.Errorf("unsupported send channel %q", target.Channel)
	}
	if target.ID == "" {
		return sendTarget{}, fmt.Errorf("send target id is required")
	}
	return target, nil
}

func sendFeishu(ctx context.Context, opts SendOptions, target sendTarget, text string) error {
	cfg, err := feishuConfigForTarget(opts.Config.Channels.Feishu, target.Account)
	if err != nil {
		return err
	}
	messageID, err := feishu.NewSender(cfg).SendText(ctx, target.Kind, target.ID, text, opts.UUID)
	if err != nil {
		return err
	}
	if opts.Out != nil {
		fmt.Fprintln(opts.Out, "channel: feishu")
		fmt.Fprintln(opts.Out, "target:", target.ID)
		if strings.TrimSpace(messageID) != "" {
			fmt.Fprintln(opts.Out, "message_id:", messageID)
		}
		fmt.Fprintln(opts.Out, "sent: true")
	}
	return nil
}

func feishuConfigForTarget(cfg config.FeishuConfig, accountID string) (config.FeishuConfig, error) {
	accounts := cfg.AccountConfigs()
	if len(accounts) == 0 {
		return config.FeishuConfig{}, fmt.Errorf("feishu channel is not configured")
	}
	accountID = strings.TrimSpace(accountID)
	for _, account := range accounts {
		if accountID == "" || strings.EqualFold(strings.TrimSpace(account.DefaultAccount), accountID) {
			if !account.Enabled {
				return config.FeishuConfig{}, fmt.Errorf("feishu account %q is disabled", account.DefaultAccount)
			}
			return account, nil
		}
	}
	return config.FeishuConfig{}, fmt.Errorf("feishu account %q not found", accountID)
}

func sendWeixin(ctx context.Context, opts SendOptions, target sendTarget, text string) error {
	cfg := opts.Config.Channels.Weixin.ResolveSecrets()
	accountID := firstNonEmpty(target.Account, cfg.AccountID)
	if accountID == "" {
		accountID = target.Account
	}
	if strings.TrimSpace(cfg.Token) == "" {
		account, err := loadWeixinAccount(opts.Config.App.Home, cfg, accountID)
		if err != nil {
			return err
		}
		cfg.AccountID = account.AccountID
		cfg.Token = account.Token
		cfg.BaseURL = firstNonEmpty(cfg.BaseURL, account.BaseURL)
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return fmt.Errorf("weixin token is required")
	}
	client := weixin.Client{BaseURL: cfg.BaseURL, Token: cfg.Token}
	resp, err := client.SendMessage(ctx, weixin.Message{
		ToUserID:     target.ID,
		ClientID:     "mateway-cli-" + time.Now().Format("20060102150405.000000000"),
		CreateTimeMS: time.Now().UnixMilli(),
		MessageType:  2,
		MessageState: 2,
		ItemList: []weixin.Item{{
			Type:     1,
			TextItem: &weixin.TextItem{Text: text},
		}},
	})
	if err != nil {
		return err
	}
	if resp.Ret != 0 || resp.ErrCode != 0 {
		return fmt.Errorf("weixin send failed: ret=%d errcode=%d errmsg=%s", resp.Ret, resp.ErrCode, resp.ErrMsg)
	}
	if opts.Out != nil {
		fmt.Fprintln(opts.Out, "channel: weixin")
		fmt.Fprintln(opts.Out, "target:", target.ID)
		fmt.Fprintln(opts.Out, "sent: true")
	}
	return nil
}

func loadWeixinAccount(home string, cfg config.WeixinConfig, accountID string) (weixin.Account, error) {
	dir := strings.TrimSpace(cfg.AccountDir)
	if dir == "" {
		dir = filepath.Join(home, "run", "weixin", "accounts")
	}
	if strings.TrimSpace(accountID) != "" {
		return weixin.LoadAccount(dir, accountID)
	}
	return weixin.LoadLatestAccount(dir)
}
