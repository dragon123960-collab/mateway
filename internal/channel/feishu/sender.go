package feishu

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
)

type Sender struct {
	client *lark.Client
}

func jsonMarshalStringContent(text string) (string, error) {
	content, err := json.Marshal(map[string]string{"text": text})
	return string(content), err
}

func NewSender(cfg config.FeishuConfig) *Sender {
	cfg = cfg.ResolveSecrets()
	options := []lark.ClientOptionFunc{}
	if strings.TrimSpace(cfg.BaseURL) != "" {
		options = append(options, lark.WithOpenBaseUrl(cfg.BaseURL))
	}
	return &Sender{client: lark.NewClient(cfg.AppID, cfg.AppSecret, options...)}
}

func (s *Sender) Reply(ctx context.Context, original channel.InboundMessage, reply channel.OutboundMessage) error {
	_, err := s.ReplyWithID(ctx, original, reply, "")
	return err
}

func (s *Sender) ReplyWithID(ctx context.Context, original channel.InboundMessage, reply channel.OutboundMessage, uuid string) (string, error) {
	if strings.TrimSpace(original.ID) == "" {
		return "", fmt.Errorf("feishu message id is required")
	}
	msgType, content, err := renderReplyMessage(reply)
	if err != nil {
		return "", err
	}
	body := larkim.NewReplyMessageReqBodyBuilder().
		MsgType(msgType).
		Content(content).
		ReplyInThread(false)
	if strings.TrimSpace(uuid) != "" {
		body.Uuid(uuid)
	}
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(original.ID).
		Body(body.Build()).
		Build()
	resp, err := s.client.Im.Message.Reply(ctx, req)
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("feishu reply failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", nil
	}
	return *resp.Data.MessageId, nil
}

func (s *Sender) ReplyTextWithID(ctx context.Context, original channel.InboundMessage, text, uuid string) (string, error) {
	if strings.TrimSpace(original.ID) == "" {
		return "", fmt.Errorf("feishu message id is required")
	}
	content, err := jsonMarshalStringContent(text)
	if err != nil {
		return "", err
	}
	body := larkim.NewReplyMessageReqBodyBuilder().
		MsgType("text").
		Content(content).
		ReplyInThread(false)
	if strings.TrimSpace(uuid) != "" {
		body.Uuid(uuid)
	}
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(original.ID).
		Body(body.Build()).
		Build()
	resp, err := s.client.Im.Message.Reply(ctx, req)
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("feishu text reply failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", nil
	}
	return *resp.Data.MessageId, nil
}

func (s *Sender) Update(ctx context.Context, messageID string, reply channel.OutboundMessage) error {
	if strings.TrimSpace(messageID) == "" {
		return fmt.Errorf("feishu message id is required")
	}
	msgType, content, err := renderReplyMessage(reply)
	if err != nil {
		return err
	}
	req := larkim.NewUpdateMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewUpdateMessageReqBodyBuilder().MsgType(msgType).Content(content).Build()).
		Build()
	resp, err := s.client.Im.Message.Update(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("feishu update failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func (s *Sender) UpdateText(ctx context.Context, messageID, text string) error {
	if strings.TrimSpace(messageID) == "" {
		return fmt.Errorf("feishu message id is required")
	}
	content, err := jsonMarshalStringContent(text)
	if err != nil {
		return err
	}
	req := larkim.NewUpdateMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewUpdateMessageReqBodyBuilder().MsgType("text").Content(content).Build()).
		Build()
	resp, err := s.client.Im.Message.Update(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("feishu text update failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func (s *Sender) React(ctx context.Context, messageID string, emojiType string) error {
	if strings.TrimSpace(messageID) == "" || strings.TrimSpace(emojiType) == "" {
		return nil
	}
	req := larkim.NewCreateMessageReactionReqBuilder().
		MessageId(messageID).
		Body(larkim.NewCreateMessageReactionReqBodyBuilder().
			ReactionType(larkim.NewEmojiBuilder().EmojiType(emojiType).Build()).
			Build()).
		Build()
	resp, err := s.client.Im.MessageReaction.Create(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("feishu reaction failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func (s *Sender) DownloadMessageImages(ctx context.Context, msg channel.InboundMessage, home string) (channel.InboundMessage, error) {
	if s == nil || s.client == nil || len(msg.Parts) == 0 {
		return msg, nil
	}
	for i, part := range msg.Parts {
		if part.Type != channel.PartImage || strings.TrimSpace(part.URI) != "" {
			continue
		}
		imageKey := strings.TrimSpace(part.Metadata["image_key"])
		if imageKey == "" {
			imageKey = strings.TrimSpace(msg.Metadata["image_key"])
		}
		if imageKey == "" {
			continue
		}
		downloaded, err := s.downloadMessageResource(ctx, msg.ID, imageKey, home)
		if err != nil {
			return msg, err
		}
		msg.Parts[i].URI = downloaded.URI
		msg.Parts[i].MimeType = downloaded.MimeType
		msg.Parts[i].Name = downloaded.Name
		msg.Parts[i].Size = downloaded.Size
		msg.Parts[i].SHA256 = downloaded.SHA256
		if msg.Parts[i].Metadata == nil {
			msg.Parts[i].Metadata = map[string]string{}
		}
		msg.Parts[i].Metadata["path"] = strings.TrimPrefix(downloaded.URI, "file://")
	}
	return msg, nil
}

type downloadedMedia struct {
	URI      string
	MimeType string
	Name     string
	Size     int64
	SHA256   string
}

func (s *Sender) downloadMessageResource(ctx context.Context, messageID, imageKey, home string) (downloadedMedia, error) {
	home = strings.TrimSpace(home)
	if home == "" {
		if userHome, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(userHome, ".mateway")
		} else {
			home = ".mateway"
		}
	}
	req := larkim.NewGetMessageResourceReqBuilder().
		MessageId(messageID).
		FileKey(imageKey).
		Type("image").
		Build()
	resp, err := s.client.Im.MessageResource.Get(ctx, req, larkcore.WithFileDownload())
	if err != nil {
		return downloadedMedia{}, err
	}
	if !resp.Success() && resp.File == nil {
		return downloadedMedia{}, fmt.Errorf("feishu image download failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	data, err := io.ReadAll(resp.File)
	if err != nil {
		return downloadedMedia{}, err
	}
	if len(data) == 0 {
		return downloadedMedia{}, fmt.Errorf("feishu image download returned empty content")
	}
	hash := sha256.Sum256(data)
	sha := hex.EncodeToString(hash[:])
	mimeType := strings.TrimSpace(resp.ApiResp.Header.Get("content-type"))
	if comma := strings.Index(mimeType, ";"); comma >= 0 {
		mimeType = strings.TrimSpace(mimeType[:comma])
	}
	if mimeType == "" {
		mimeType = httpDetectContentType(data)
	}
	ext := strings.ToLower(filepath.Ext(resp.FileName))
	if ext == "" {
		ext = extensionForMime(mimeType)
	}
	dir := filepath.Join(home, "media", "feishu", time.Now().Format("20060102"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return downloadedMedia{}, err
	}
	base := safeMediaName(firstNonEmpty(messageID, imageKey)) + "-" + sha[:12] + ext
	path := filepath.Join(dir, base)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return downloadedMedia{}, err
	}
	return downloadedMedia{URI: "file://" + path, MimeType: mimeType, Name: base, Size: int64(len(data)), SHA256: sha}, nil
}

func httpDetectContentType(data []byte) string {
	if len(data) == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(data)
}

func extensionForMime(mimeType string) string {
	if exts, err := mime.ExtensionsByType(mimeType); err == nil && len(exts) > 0 {
		return exts[0]
	}
	switch mimeType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".bin"
	}
}

func safeMediaName(value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "media"
	}
	return b.String()
}
