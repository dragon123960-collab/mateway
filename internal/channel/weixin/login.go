package weixin

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

	"github.com/dongping/mateway/internal/config"
	"github.com/mdp/qrterminal/v3"
	"gopkg.in/yaml.v3"
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
	qrURL := firstNonEmpty(qr.QRCodeImgContent, qr.QRCodeURL, qr.QRCode)
	if out != nil {
		fmt.Fprintln(out, "请使用微信扫描以下二维码：")
		qrterminal.GenerateHalfBlock(qrURL, qrterminal.L, out)
		if qr.QRCodeImgContent != "" || qr.QRCodeURL != "" {
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
			if err := EnableConfig(home, account); err != nil && out != nil {
				fmt.Fprintf(out, "\n微信凭据已保存，但更新 weixin.yaml 失败：%v\n", err)
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

func EnableSavedAccount(cfg config.WeixinConfig, home, accountID string) (Account, error) {
	cfg = cfg.ResolveSecrets()
	accountDir := strings.TrimSpace(cfg.AccountDir)
	if accountDir == "" {
		accountDir = defaultAccountDir(home)
	}
	var account Account
	var err error
	if strings.TrimSpace(accountID) != "" {
		account, err = LoadAccount(accountDir, accountID)
	} else {
		account, err = LoadLatestAccount(accountDir)
	}
	if err != nil {
		return Account{}, err
	}
	if err := EnableConfig(home, account); err != nil {
		return Account{}, err
	}
	return account, nil
}

func EnableConfig(home string, account Account) error {
	path := filepath.Join(home, "config", "channels", "weixin.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return err
	}
	doc := documentMapping(&root)
	if doc == nil {
		return fmt.Errorf("weixin.yaml must contain a mapping")
	}
	weixinNode := mappingValue(doc, "weixin")
	if weixinNode == nil {
		weixinNode = &yaml.Node{Kind: yaml.MappingNode}
		doc.Content = append(doc.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "weixin"}, weixinNode)
	}
	setMappingValue(weixinNode, "enabled", "true", "!!bool")
	setMappingValue(weixinNode, "account_id", account.AccountID, "!!str")
	setMappingValue(weixinNode, "base_url", account.BaseURL, "!!str")
	out, err := yaml.Marshal(&root)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

func documentMapping(node *yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		return node.Content[0]
	}
	if node.Kind == yaml.MappingNode {
		return node
	}
	return nil
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func setMappingValue(mapping *yaml.Node, key, value, tag string) {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1].Kind = yaml.ScalarNode
			mapping.Content[i+1].Value = value
			mapping.Content[i+1].Tag = tag
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key, Tag: "!!str"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: value, Tag: tag},
	)
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
