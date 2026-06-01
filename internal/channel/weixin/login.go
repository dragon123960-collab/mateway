package weixin

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/config"
)

func Login(ctx context.Context, cfg config.WeixinConfig, home, botType string, timeout time.Duration, out io.Writer) (Account, string, error) {
	cfg = cfg.ResolveSecrets()
	baseURL := firstNonEmpty(cfg.BaseURL, defaultBaseURL)
	client := Client{BaseURL: baseURL}
	qr, err := client.GetQRCode(ctx, botType)
	if err != nil {
		return Account{}, "", err
	}
	qrCode := strings.TrimSpace(qr.QRCode)
	if qrCode == "" {
		return Account{}, "", fmt.Errorf("weixin QR response missing qrcode")
	}
	qrURL := firstNonEmpty(qr.QRCodeURL, qr.QRCode)
	if out != nil {
		fmt.Fprintln(out, "请使用微信扫描以下二维码链接：")
		fmt.Fprintln(out, qrURL)
		fmt.Fprintln(out, "扫码后请在微信里确认。")
	}
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	currentBaseURL := baseURL
	for time.Now().Before(deadline) {
		statusClient := Client{BaseURL: currentBaseURL}
		status, err := statusClient.GetQRStatus(ctx, qrCode)
		if err != nil {
			sleepOrDone(ctx, time.Second)
			continue
		}
		switch strings.TrimSpace(fmt.Sprint(status["status"])) {
		case "wait":
			if out != nil {
				fmt.Fprint(out, ".")
			}
		case "scaned":
			if out != nil {
				fmt.Fprintln(out, "\n已扫码，请在微信里确认...")
			}
		case "scaned_but_redirect":
			if host := mapString(status, "redirect_host"); host != "" {
				currentBaseURL = "https://" + host
			}
		case "confirmed":
			account := Account{
				AccountID: firstNonEmpty(mapString(status, "ilink_bot_id"), qr.AccountID),
				Token:     firstNonEmpty(mapString(status, "bot_token"), qr.Token),
				BaseURL:   firstNonEmpty(mapString(status, "baseurl"), qr.BaseURL, currentBaseURL),
				CreatedAt: time.Now().UTC().Format(time.RFC3339),
			}
			if account.AccountID == "" || account.Token == "" {
				return Account{}, qrURL, fmt.Errorf("weixin QR confirmed but account_id/token are missing")
			}
			accountDir := strings.TrimSpace(cfg.AccountDir)
			if accountDir == "" {
				accountDir = defaultAccountDir(home)
			}
			if err := SaveAccount(accountDir, account); err != nil {
				return Account{}, qrURL, err
			}
			if out != nil {
				fmt.Fprintln(out, "\n微信连接成功。")
			}
			return account, qrURL, nil
		case "expired":
			return Account{}, qrURL, fmt.Errorf("weixin QR code expired")
		}
		sleepOrDone(ctx, time.Second)
	}
	return Account{}, qrURL, fmt.Errorf("weixin login timed out")
}

func mapString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}
