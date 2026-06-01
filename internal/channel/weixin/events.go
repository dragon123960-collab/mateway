package weixin

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
)

type Receiver func(context.Context, channel.InboundMessage) (channel.OutboundMessage, error)

func Start(ctx context.Context, cfg config.WeixinConfig, home string, receiver Receiver) error {
	cfg = cfg.ResolveSecrets()
	if !cfg.Enabled {
		return fmt.Errorf("weixin channel is disabled")
	}
	accountDir := strings.TrimSpace(cfg.AccountDir)
	if accountDir == "" {
		accountDir = defaultAccountDir(home)
	}
	if strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.Token) == "" {
		if account, err := LoadAccount(accountDir, cfg.AccountID); err == nil {
			cfg.AccountID = firstNonEmpty(cfg.AccountID, account.AccountID)
			cfg.Token = firstNonEmpty(cfg.Token, account.Token)
			cfg.BaseURL = firstNonEmpty(cfg.BaseURL, account.BaseURL)
		}
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if strings.TrimSpace(cfg.Token) == "" || strings.TrimSpace(cfg.AccountID) == "" {
		return fmt.Errorf("weixin account_id/token are required; run `mateway weixin login` first")
	}
	retry := parseDuration(cfg.RetryInterval, 3*time.Second)
	pollTimeout := time.Duration(cfg.PollTimeoutMS) * time.Millisecond
	if pollTimeout <= 0 {
		pollTimeout = 35 * time.Second
	}
	client := Client{BaseURL: cfg.BaseURL, Token: cfg.Token}
	syncBuf := loadSyncBuf(accountDir, cfg.AccountID)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		resp, err := client.GetUpdates(ctx, syncBuf, pollTimeout+5*time.Second)
		if err != nil {
			log.Printf("mateway weixin getupdates error account=%s: %v", safeID(cfg.AccountID), err)
			sleepOrDone(ctx, retry)
			continue
		}
		if resp.Ret == sessionExpiredErrCode || resp.ErrCode == sessionExpiredErrCode {
			log.Printf("mateway weixin session expired account=%s; re-login required", safeID(cfg.AccountID))
			sleepOrDone(ctx, 10*time.Minute)
			continue
		}
		if resp.Ret != 0 || resp.ErrCode != 0 {
			log.Printf("mateway weixin getupdates failed account=%s ret=%d errcode=%d errmsg=%s", safeID(cfg.AccountID), resp.Ret, resp.ErrCode, resp.ErrMsg)
			sleepOrDone(ctx, retry)
			continue
		}
		if strings.TrimSpace(resp.GetUpdatesBuf) != "" {
			syncBuf = resp.GetUpdatesBuf
			saveSyncBuf(accountDir, cfg.AccountID, syncBuf)
		}
		for _, message := range resp.Msgs {
			msg, ok := message.ToInbound(cfg.AccountID)
			if !ok {
				continue
			}
			go handleMessage(ctx, client, msg, receiver)
		}
	}
}

func handleMessage(ctx context.Context, client Client, msg channel.InboundMessage, receiver Receiver) {
	reply, err := receiver(ctx, msg)
	if err != nil {
		log.Printf("mateway weixin receiver error message_id=%s: %v", msg.ID, err)
		return
	}
	if strings.TrimSpace(reply.Text) == "" {
		return
	}
	resp, err := client.SendMessage(ctx, ReplyToMessage(msg, reply))
	if err != nil {
		log.Printf("mateway weixin sendmessage error message_id=%s: %v", msg.ID, err)
		return
	}
	if resp.Ret != 0 || resp.ErrCode != 0 {
		log.Printf("mateway weixin sendmessage failed message_id=%s ret=%d errcode=%d errmsg=%s", msg.ID, resp.Ret, resp.ErrCode, resp.ErrMsg)
	}
}

func sleepOrDone(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func parseDuration(value string, fallback time.Duration) time.Duration {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return parsed
}

func safeID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 8 {
		return value
	}
	return value[:8]
}
