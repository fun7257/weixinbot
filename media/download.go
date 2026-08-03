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
	CDN      *CDN
	Store    *Store
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

func downloadImage(ctx context.Context, item protocol.MessageItem, deps DownloadDeps) (*LocalMedia, error) {
	img := item.ImageItem
	if img == nil || img.Media == nil {
		return nil, fmt.Errorf("media: image missing media")
	}
	if img.Media.FullURL == "" && img.Media.EncryptQueryParam == "" {
		return nil, fmt.Errorf("media: image missing URL")
	}
	hexKey := strings.TrimSpace(img.AESKey)
	b64Key := ""
	if img.Media != nil {
		b64Key = strings.TrimSpace(img.Media.AESKey)
	}
	// Plain download only when no key material is present at all.
	if hexKey == "" && b64Key == "" {
		ct, err := deps.CDN.DownloadCiphertext(ctx, img.Media.FullURL, img.Media.EncryptQueryParam)
		if err != nil {
			return nil, err
		}
		path, err := deps.Store.Save("inbound", "image.bin", ct)
		if err != nil {
			return nil, err
		}
		return &LocalMedia{Kind: "image", Path: path, MIME: "application/octet-stream", FileName: filepath.Base(path)}, nil
	}
	// Fail closed on malformed keys (do not save ciphertext as an image).
	key, err := resolveAESKey(hexKey, b64Key)
	if err != nil {
		return nil, err
	}
	plain, err := DownloadAndDecrypt(ctx, deps.CDN, img.Media.FullURL, img.Media.EncryptQueryParam, key)
	if err != nil {
		return nil, err
	}
	path, err := deps.Store.Save("inbound", "image.bin", plain)
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
		return nil, fmt.Errorf("media: voice missing URL")
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
			path, err := deps.Store.Save("inbound", "voice.wav", wav)
			if err != nil {
				return nil, err
			}
			return &LocalMedia{Kind: "voice", Path: path, MIME: "audio/wav", FileName: filepath.Base(path)}, nil
		}
	}
	path, err := deps.Store.Save("inbound", "voice.silk", silkBuf)
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
	path, err := deps.Store.Save("inbound", name, plain)
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
	path, err := deps.Store.Save("inbound", "video.mp4", plain)
	if err != nil {
		return nil, err
	}
	return &LocalMedia{Kind: "video", Path: path, MIME: "video/mp4", FileName: filepath.Base(path)}, nil
}

// resolveAESKey prefers hex key (image_item.aeskey), else base64 media.aes_key.
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
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64Key))
	if err != nil {
		// try raw URL encoding
		b, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(b64Key))
		if err != nil {
			return nil, fmt.Errorf("media: bad aes_key base64: %w", err)
		}
	}
	if len(b) != 16 {
		return nil, fmt.Errorf("media: aes_key must be 16 bytes, got %d", len(b))
	}
	return b, nil
}
