package weixin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/config"
	"github.com/mdp/qrterminal/v3"
)

func Login(ctx context.Context, cfg config.WeixinConfig, home, botType string, timeout time.Duration, out io.Writer) (Account, string, error) {
	cfg = cfg.ResolveSecrets()
	baseURL := firstNonEmpty(cfg.BaseURL, defaultBaseURL)
	if strings.TrimSpace(botType) == "" {
		botType = "3"
	}
	client := Client{BaseURL: baseURL}
	qr, err := client.GetQRCode(ctx, botType)
	if err != nil {
		return Account{}, "", err
	}
	qrCode := strings.TrimSpace(qr.QRCode)
	if qrCode == "" {
		return Account{}, "", fmt.Errorf("weixin QR response missing qrcode: ret=%d errcode=%d errmsg=%s qrcode_url=%q", qr.Ret, qr.ErrCode, qr.ErrMsg, qr.QRCodeURL)
	}
	qrURL := firstNonEmpty(qr.QRCodeURL, qr.QRCode)
	if out != nil {
		fmt.Fprintln(out, "请使用微信扫描以下二维码：")
		qrterminal.GenerateHalfBlock(qrURL, qrterminal.L, out)
		if qr.QRCodeURL != "" {
			fmt.Fprintln(out, "二维码链接：")
		} else {
			fmt.Fprintln(out, "二维码 token：")
		}
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
		statusText := strings.TrimSpace(fmt.Sprint(status["status"]))
		errCode := intFromAny(status["errcode"])
		switch {
		case statusText == "wait" || errCode == -22:
			if out != nil {
				fmt.Fprint(out, ".")
			}
		case statusText == "scaned" || errCode == -13:
			if out != nil {
				fmt.Fprintln(out, "\n已扫码，请在微信里确认...")
			}
		case statusText == "scaned_but_redirect":
			if host := mapString(status, "redirect_host"); host != "" {
				currentBaseURL = "https://" + host
			}
		case statusText == "confirmed" || errCode == 0:
			account := Account{
				AccountID: firstNonEmpty(mapString(status, "ilink_bot_id"), mapString(status, "account_id"), qr.AccountID),
				Token:     firstNonEmpty(mapString(status, "bot_token"), mapString(status, "token"), qr.Token),
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
		case statusText == "expired":
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

func intFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		got, _ := typed.Int64()
		return int(got)
	case string:
		got, _ := strconv.Atoi(strings.TrimSpace(typed))
		return got
	default:
		return 0
	}
}
