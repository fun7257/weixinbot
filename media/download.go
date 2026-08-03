package media

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tencent-weixin/weixinbot/protocol"
)

// LocalMedia is a downloaded/decrypted inbound media attachment.
type LocalMedia struct {
	Kind     string // image | voice | file | video
	Path     string
	MIME     string
	FileName string
}

// DownloadDeps configures inbound media download.
type DownloadDeps struct {
	CDN   *CDN
	Store *Store
	// AccountID scopes saved files under the media store (optional; defaults to "inbound").
	AccountID string
	// SilkToWAV optional transcoder; nil or error → keep raw silk.
	SilkToWAV func(silk []byte) (wav []byte, err error)
}

// DownloadItem downloads and decrypts media from a MessageItem.
// Returns (nil, nil) for non-media items; (nil, err) on hard failures when media was expected.
func DownloadItem(ctx context.Context, item protocol.MessageItem, deps DownloadDeps) (*LocalMedia, error) {
	if deps.CDN == nil || deps.Store == nil {
		return nil, fmt.Errorf("media: DownloadItem requires CDN and Store")
	}
	switch item.Type {
	case protocol.ItemTypeImage:
		return downloadImage(ctx, item, deps)
	case protocol.ItemTypeVoice:
		return downloadVoice(ctx, item, deps)
	case protocol.ItemTypeFile:
		return downloadFile(ctx, item, deps)
	case protocol.ItemTypeVideo:
		return downloadVideo(ctx, item, deps)
	default:
		return nil, nil
	}
}

func accountKey(deps DownloadDeps) string {
	if strings.TrimSpace(deps.AccountID) != "" {
		return deps.AccountID
	}
	return "inbound"
}

func downloadImage(ctx context.Context, item protocol.MessageItem, deps DownloadDeps) (*LocalMedia, error) {
	img := item.ImageItem
	if img == nil {
		return nil, fmt.Errorf("media: image missing image_item")
	}
	// Prefer main media; fall back to thumb when HD media is absent (some clients).
	m := img.Media
	if m == nil || (m.FullURL == "" && m.EncryptQueryParam == "") {
		m = img.ThumbMedia
	}
	if m == nil || (m.FullURL == "" && m.EncryptQueryParam == "") {
		return nil, fmt.Errorf("media: image missing media URL")
	}

	// openclaw-weixin: prefer image_item.aeskey (hex) → base64(raw), else media.aes_key.
	key, err := resolveImageAESKey(img.AESKey, m.AESKey)
	if err != nil {
		// No key: try plain download (encrypt_type=0 or already-clear objects).
		if strings.TrimSpace(img.AESKey) == "" && strings.TrimSpace(m.AESKey) == "" {
			ct, err := deps.CDN.DownloadCiphertext(ctx, m.FullURL, m.EncryptQueryParam)
			if err != nil {
				return nil, err
			}
			path, err := deps.Store.Save(accountKey(deps), "image.bin", ct)
			if err != nil {
				return nil, err
			}
			return &LocalMedia{Kind: "image", Path: path, MIME: "application/octet-stream", FileName: filepath.Base(path)}, nil
		}
		return nil, err
	}
	plain, err := DownloadAndDecrypt(ctx, deps.CDN, m.FullURL, m.EncryptQueryParam, key)
	if err != nil {
		return nil, err
	}
	path, err := deps.Store.Save(accountKey(deps), "image.jpg", plain)
	if err != nil {
		return nil, err
	}
	return &LocalMedia{Kind: "image", Path: path, MIME: "image/jpeg", FileName: filepath.Base(path)}, nil
}

func downloadVoice(ctx context.Context, item protocol.MessageItem, deps DownloadDeps) (*LocalMedia, error) {
	v := item.VoiceItem
	if v == nil || v.Media == nil {
		return nil, fmt.Errorf("media: voice missing media")
	}
	if v.Media.FullURL == "" && v.Media.EncryptQueryParam == "" {
		// ASR-only voice (text filled, no CDN ref) — not an error; caller may use Text.
		if v.Text != "" {
			return nil, nil
		}
		return nil, fmt.Errorf("media: voice missing URL")
	}
	if strings.TrimSpace(v.Media.AESKey) == "" {
		if v.Text != "" {
			return nil, nil
		}
		return nil, fmt.Errorf("media: voice missing aes_key")
	}
	key, err := resolveAESKey("", v.Media.AESKey)
	if err != nil {
		return nil, err
	}
	silkBuf, err := DownloadAndDecrypt(ctx, deps.CDN, v.Media.FullURL, v.Media.EncryptQueryParam, key)
	if err != nil {
		return nil, err
	}
	if deps.SilkToWAV != nil {
		if wav, err := deps.SilkToWAV(silkBuf); err == nil && len(wav) > 0 {
			path, err := deps.Store.Save(accountKey(deps), "voice.wav", wav)
			if err != nil {
				return nil, err
			}
			return &LocalMedia{Kind: "voice", Path: path, MIME: "audio/wav", FileName: filepath.Base(path)}, nil
		}
	}
	path, err := deps.Store.Save(accountKey(deps), "voice.silk", silkBuf)
	if err != nil {
		return nil, err
	}
	return &LocalMedia{Kind: "voice", Path: path, MIME: "audio/silk", FileName: filepath.Base(path)}, nil
}

func downloadFile(ctx context.Context, item protocol.MessageItem, deps DownloadDeps) (*LocalMedia, error) {
	fi := item.FileItem
	if fi == nil || fi.Media == nil {
		return nil, fmt.Errorf("media: file missing media")
	}
	if fi.Media.FullURL == "" && fi.Media.EncryptQueryParam == "" {
		return nil, fmt.Errorf("media: file missing URL")
	}
	key, err := resolveAESKey("", fi.Media.AESKey)
	if err != nil {
		return nil, err
	}
	plain, err := DownloadAndDecrypt(ctx, deps.CDN, fi.Media.FullURL, fi.Media.EncryptQueryParam, key)
	if err != nil {
		return nil, err
	}
	name := fi.FileName
	if name == "" {
		name = "file.bin"
	}
	path, err := deps.Store.Save(accountKey(deps), name, plain)
	if err != nil {
		return nil, err
	}
	return &LocalMedia{Kind: "file", Path: path, MIME: MIMEFromFilename(name), FileName: name}, nil
}

func downloadVideo(ctx context.Context, item protocol.MessageItem, deps DownloadDeps) (*LocalMedia, error) {
	vi := item.VideoItem
	if vi == nil || vi.Media == nil {
		return nil, fmt.Errorf("media: video missing media")
	}
	if vi.Media.FullURL == "" && vi.Media.EncryptQueryParam == "" {
		return nil, fmt.Errorf("media: video missing URL")
	}
	key, err := resolveAESKey("", vi.Media.AESKey)
	if err != nil {
		return nil, err
	}
	plain, err := DownloadAndDecrypt(ctx, deps.CDN, vi.Media.FullURL, vi.Media.EncryptQueryParam, key)
	if err != nil {
		return nil, err
	}
	path, err := deps.Store.Save(accountKey(deps), "video.mp4", plain)
	if err != nil {
		return nil, err
	}
	return &LocalMedia{Kind: "video", Path: path, MIME: "video/mp4", FileName: filepath.Base(path)}, nil
}

// resolveImageAESKey mirrors openclaw-weixin media-download image key selection:
//  1. image_item.aeskey (hex) → raw 16 bytes
//  2. else media.aes_key (base64 raw-16 or base64(hex-ascii))
func resolveImageAESKey(imageAESKeyHex, mediaAESKeyB64 string) ([]byte, error) {
	hexKey := strings.TrimSpace(imageAESKeyHex)
	if hexKey != "" {
		// Primary: hex string of 16-byte key (32 hex chars).
		if b, err := hex.DecodeString(hexKey); err == nil && len(b) == 16 {
			return b, nil
		}
		// Some payloads put base64 into aeskey by mistake — try media-style parse.
		if b, err := resolveAESKey("", hexKey); err == nil {
			return b, nil
		}
	}
	return resolveAESKey("", mediaAESKeyB64)
}

// resolveAESKey prefers hex key (image_item.aeskey), else base64 media.aes_key.
//
// media.aes_key encodings seen in production (openclaw-weixin parseAesKey):
//   - base64(raw 16-byte key)           — common for images
//   - base64(ASCII hex of 16-byte key)  — file / voice / video
func resolveAESKey(hexKey, b64Key string) ([]byte, error) {
	if strings.TrimSpace(hexKey) != "" {
		b, err := hex.DecodeString(strings.TrimSpace(hexKey))
		if err != nil {
			return nil, fmt.Errorf("media: bad aeskey hex: %w", err)
		}
		if len(b) != 16 {
			return nil, fmt.Errorf("media: aeskey hex must be 16 bytes, got %d", len(b))
		}
		return b, nil
	}
	if strings.TrimSpace(b64Key) == "" {
		return nil, fmt.Errorf("media: missing aes key")
	}
	raw := strings.TrimSpace(b64Key)
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		b, err = base64.RawStdEncoding.DecodeString(raw)
		if err != nil {
			// Last resort: treat as plain hex without base64 wrapping.
			if hb, herr := hex.DecodeString(raw); herr == nil && len(hb) == 16 {
				return hb, nil
			}
			return nil, fmt.Errorf("media: bad aes_key base64: %w", err)
		}
	}
	if len(b) == 16 {
		return b, nil
	}
	// base64(hex string): 32 ASCII hex chars → 16 raw bytes
	if len(b) == 32 {
		s := string(b)
		if isASCIIHex32(s) {
			rawKey, err := hex.DecodeString(s)
			if err != nil {
				return nil, fmt.Errorf("media: bad aes_key hex-in-base64: %w", err)
			}
			return rawKey, nil
		}
	}
	return nil, fmt.Errorf("media: aes_key must be 16 raw bytes or 32-char hex after base64, got %d bytes", len(b))
}

func isASCIIHex32(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < 32; i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
