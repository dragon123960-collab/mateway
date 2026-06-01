package weixin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultBaseURL        = "https://ilinkai.weixin.qq.com"
	channelVersion        = "2.2.0"
	iLinkAppID            = "bot"
	iLinkClientVersion    = (2 << 16) | (2 << 8) | 0
	endpointGetUpdates    = "ilink/bot/getupdates"
	endpointSendMessage   = "ilink/bot/sendmessage"
	endpointGetBotQR      = "ilink/bot/get_bot_qrcode"
	endpointGetQRStatus   = "ilink/bot/get_qrcode_status"
	sessionExpiredErrCode = -14
)

type Client struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

func (c Client) GetUpdates(ctx context.Context, syncBuf string, timeout time.Duration) (GetUpdatesResponse, error) {
	var out GetUpdatesResponse
	err := c.post(ctx, endpointGetUpdates, map[string]any{"get_updates_buf": syncBuf}, timeout, &out)
	return out, err
}

func (c Client) SendMessage(ctx context.Context, message Message) (SendMessageResponse, error) {
	var out SendMessageResponse
	err := c.post(ctx, endpointSendMessage, map[string]any{"msg": message}, 15*time.Second, &out)
	return out, err
}

func (c Client) GetQRCode(ctx context.Context, botType string) (LoginStartResponse, error) {
	var out LoginStartResponse
	endpoint := endpointGetBotQR
	if strings.TrimSpace(botType) != "" {
		endpoint += "?bot_type=" + url.QueryEscape(strings.TrimSpace(botType))
	}
	err := c.get(ctx, endpoint, 35*time.Second, &out)
	return out, err
}

func (c Client) GetQRStatus(ctx context.Context, qrCode string) (map[string]any, error) {
	var out map[string]any
	endpoint := endpointGetQRStatus + "?qrcode=" + url.QueryEscape(strings.TrimSpace(qrCode))
	err := c.get(ctx, endpoint, 35*time.Second, &out)
	return out, err
}

func (c Client) post(ctx context.Context, endpoint string, payload map[string]any, timeout time.Duration, out any) error {
	payload["base_info"] = map[string]any{"channel_version": channelVersion}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(endpoint), bytes.NewReader(body))
	if err != nil {
		return err
	}
	for key, value := range iLinkHeaders(c.Token, body) {
		req.Header.Set(key, value)
	}
	resp, err := c.httpClient(timeout).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("iLink POST %s failed: status=%d", endpoint, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c Client) get(ctx context.Context, endpoint string, timeout time.Duration, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(endpoint), nil)
	if err != nil {
		return err
	}
	req.Header.Set("iLink-App-Id", iLinkAppID)
	req.Header.Set("iLink-App-ClientVersion", fmt.Sprintf("%d", iLinkClientVersion))
	resp, err := c.httpClient(timeout).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("iLink GET %s failed: status=%d", endpoint, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c Client) url(endpoint string) string {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = defaultBaseURL
	}
	return base + "/" + strings.TrimLeft(endpoint, "/")
}

func (c Client) httpClient(timeout time.Duration) *http.Client {
	if c.Client != nil {
		return c.Client
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &http.Client{Timeout: timeout}
}

func iLinkHeaders(token string, body []byte) map[string]string {
	headers := map[string]string{
		"Content-Type":            "application/json",
		"AuthorizationType":       "ilink_bot_token",
		"Content-Length":          fmt.Sprintf("%d", len(body)),
		"X-WECHAT-UIN":            randomWechatUIN(),
		"iLink-App-Id":            iLinkAppID,
		"iLink-App-ClientVersion": fmt.Sprintf("%d", iLinkClientVersion),
	}
	if strings.TrimSpace(token) != "" {
		headers["Authorization"] = "Bearer " + strings.TrimSpace(token)
	}
	return headers
}

func randomWechatUIN() string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return base64.StdEncoding.EncodeToString([]byte("0"))
	}
	value := binary.BigEndian.Uint32(buf[:])
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d", value)))
}
