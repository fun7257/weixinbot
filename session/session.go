// Package session is the bot runtime: long-poll inbound, outbound send with session glue.
//
// Architecture (protocol-driven, not OpenClaw):
//   - Inbound: client long-polls getupdates, advances get_updates_buf, stores peer state.
//   - Outbound: client POSTs sendmessage (and CDN for media), attaching stored context_token.
//   - Policy: hard-block sendmessage when session window/quota exceeded.
package session

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tencent-weixin/weixinbot/ilink"
	"github.com/tencent-weixin/weixinbot/markdown"
	"github.com/tencent-weixin/weixinbot/media"
	"github.com/tencent-weixin/weixinbot/protocol"
	"github.com/tencent-weixin/weixinbot/state"
)

// Handler is invoked for each inbound message (sequentially in the poll loop).
type Handler func(ctx context.Context, msg InboundMessage) error

// Options configures a Session.
type Options struct {
	AccountID  string
	Client     *ilink.Client
	Store      *state.Store
	CDNBaseURL string
	HTTP       ilink.Doer // shared fake for CGI+CDN in tests; optional
	Handler    Handler
	Logger     *slog.Logger

	// LongPollTimeout is the client-side hold for getupdates (default 35s).
	LongPollTimeout time.Duration
	// RetryDelay after transient poll errors.
	RetryDelay time.Duration
	// StaleTokenPause is pause duration on -14 (default 1h).
	StaleTokenPause time.Duration

	// Policy hard-blocks outbound when window/quota exceeded (defaults enabled).
	Policy OutboundPolicy

	// TextChunkLimit default 4000 runes.
	TextChunkLimit int
	// RetryRateLimit times to retry sendmessage on ret=-2 (default 0).
	RetryRateLimit int
	// RateLimitBackoff initial backoff (default 200ms).
	RateLimitBackoff time.Duration

	// MediaStore for inbound downloads; if nil a temp dir under Store root is used.
	MediaStore *media.Store
	// SilkCodec for voice encode/decode; nil uses media.DefaultSilkCodec.
	SilkCodec media.SilkCodec

	// OnHandlerError is called when Handler returns error (default: slog).
	OnHandlerError func(msg InboundMessage, err error)
	// OnMediaError is called when inbound media download fails (message still delivered).
	OnMediaError func(msg InboundMessage, err error)

	// MaxMediaDownloadBytes overrides media store + CDN download limit (default media.DefaultMaxBytes).
	MaxMediaDownloadBytes int64
	// MaxMediaURLBytes for SendMediaURL (default 50MiB).
	MaxMediaURLBytes int64

	// AllowAnyCDNFullURL disables CDN full_url / upload_full_url host pinning.
	// Tests only — never enable in production.
	AllowAnyCDNFullURL bool
	// MediaURLSkipDNSCheck skips DNS resolution in SendMediaURL SSRF checks.
	// Tests with fake hosts + RoundTripper only; default false (fail closed on lookup error).
	MediaURLSkipDNSCheck bool

	// TypingKeepalive interval (default 5s).
	TypingKeepalive time.Duration

	// Clock for tests (policy + pause).
	Now func() time.Time
}

// Session is one bot account's live communication loop.
type Session struct {
	opts   Options
	cdn    *media.CDN
	log    *slog.Logger
	cfg    *ConfigCache
	media  *media.Store
	mu     sync.Mutex
	paused time.Time // zero if not paused
}

// New validates options and returns a Session (does not start polling).
func New(opts Options) (*Session, error) {
	if opts.AccountID == "" {
		return nil, errors.New("session: AccountID required")
	}
	if opts.Client == nil {
		return nil, errors.New("session: Client required")
	}
	if opts.Store == nil {
		return nil, errors.New("session: Store required")
	}
	if opts.CDNBaseURL == "" {
		opts.CDNBaseURL = ilink.DefaultCDNBaseURL
	}
	if opts.LongPollTimeout <= 0 {
		opts.LongPollTimeout = 35 * time.Second
	}
	if opts.RetryDelay <= 0 {
		opts.RetryDelay = 2 * time.Second
	}
	if opts.StaleTokenPause <= 0 {
		opts.StaleTokenPause = time.Hour
	}
	if opts.TextChunkLimit <= 0 {
		opts.TextChunkLimit = 4000
	}
	if opts.RateLimitBackoff <= 0 {
		opts.RateLimitBackoff = 200 * time.Millisecond
	}
	if opts.TypingKeepalive <= 0 {
		opts.TypingKeepalive = 5 * time.Second
	}
	if opts.MaxMediaURLBytes <= 0 {
		opts.MaxMediaURLBytes = 50 * 1024 * 1024
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	opts.Policy.Now = opts.Now
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	cdnHTTP := opts.HTTP
	ms := opts.MediaStore
	if ms == nil {
		// place under a sibling of store is not accessible; use OS temp
		dir, err := os.MkdirTemp("", "weixinbot-media-*")
		if err != nil {
			return nil, err
		}
		ms, err = media.NewStore(dir)
		if err != nil {
			return nil, err
		}
	}
	// Align MediaStore and CDN download caps on the same default unless overridden.
	maxBytes := int64(media.DefaultMaxBytes)
	if opts.MaxMediaDownloadBytes > 0 {
		maxBytes = opts.MaxMediaDownloadBytes
	}
	ms.MaxBytes = maxBytes
	cdn := &media.CDN{
		BaseURL:          opts.CDNBaseURL,
		HTTP:             cdnHTTP,
		MaxDownloadBytes: maxBytes,
		// Explicit opt-in only — never auto-enable from HTTP injection.
		AllowAnyFullURL: opts.AllowAnyCDNFullURL,
	}
	s := &Session{
		opts:  opts,
		cdn:   cdn,
		log:   log,
		cfg:   NewConfigCache(opts.Client),
		media: ms,
	}
	if opts.OnHandlerError == nil {
		s.opts.OnHandlerError = func(msg InboundMessage, err error) {
			log.Error("handler", "err", err, "from", msg.FromUserID)
		}
	}
	if opts.OnMediaError == nil {
		s.opts.OnMediaError = func(msg InboundMessage, err error) {
			log.Warn("media download", "err", err, "from", msg.FromUserID)
		}
	}
	return s, nil
}

// AccountID returns the session account id.
func (s *Session) AccountID() string { return s.opts.AccountID }

// Client returns the underlying iLink client.
func (s *Session) Client() *ilink.Client { return s.opts.Client }

// Store returns the durable state store.
func (s *Session) Store() *state.Store { return s.opts.Store }

// Run long-polls until ctx is cancelled. It is the primary inbound entry point.
func (s *Session) Run(ctx context.Context) error {
	if err := s.opts.Store.RestoreContextTokens(s.opts.AccountID); err != nil {
		s.log.Warn("restore context tokens", "err", err)
	}

	if _, err := s.opts.Client.NotifyStart(ctx); err != nil {
		s.log.Debug("notifyStart", "err", err)
	}

	buf, err := s.opts.Store.LoadSyncBuf(s.opts.AccountID)
	if err != nil {
		return fmt.Errorf("session: load sync buf: %w", err)
	}

	timeout := s.opts.LongPollTimeout
	for {
		if err := ctx.Err(); err != nil {
			_, _ = s.opts.Client.NotifyStop(context.Background())
			return err
		}
		if wait := s.pauseRemaining(); wait > 0 {
			s.log.Info("session paused (stale token)", "wait", wait)
			select {
			case <-ctx.Done():
				_, _ = s.opts.Client.NotifyStop(context.Background())
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}

		resp, err := s.opts.Client.GetUpdates(ctx, buf, timeout)
		if err != nil {
			if ctx.Err() != nil {
				_, _ = s.opts.Client.NotifyStop(context.Background())
				return ctx.Err()
			}
			s.log.Warn("getUpdates error", "err", err)
			select {
			case <-ctx.Done():
				_, _ = s.opts.Client.NotifyStop(context.Background())
				return ctx.Err()
			case <-time.After(s.opts.RetryDelay):
			}
			continue
		}

		if resp.LongPollingTimeoutMs > 0 {
			timeout = time.Duration(resp.LongPollingTimeoutMs) * time.Millisecond
		}

		if isAPIFailure(resp) {
			if resp.ErrCode == protocol.StaleTokenErrCode || resp.Ret == protocol.StaleTokenErrCode {
				s.pauseFor(s.opts.StaleTokenPause)
				continue
			}
			s.log.Warn("getUpdates api error", "ret", resp.Ret, "errcode", resp.ErrCode, "errmsg", resp.ErrMsg)
			select {
			case <-ctx.Done():
				_, _ = s.opts.Client.NotifyStop(context.Background())
				return ctx.Err()
			case <-time.After(s.opts.RetryDelay):
			}
			continue
		}

		if resp.GetUpdatesBuf != "" {
			if err := s.opts.Store.SaveSyncBuf(s.opts.AccountID, resp.GetUpdatesBuf); err != nil {
				s.log.Warn("save sync buf", "err", err)
			} else {
				buf = resp.GetUpdatesBuf
			}
		}

		for _, raw := range resp.Msgs {
			if err := s.dispatch(ctx, raw); err != nil {
				// dispatch only returns handler errors; already notified via callback
				_ = err
			}
		}
	}
}

func isAPIFailure(resp *protocol.GetUpdatesResp) bool {
	if resp == nil {
		return true
	}
	if resp.Ret != 0 {
		return true
	}
	if resp.ErrCode != 0 {
		return true
	}
	return false
}

func (s *Session) dispatch(ctx context.Context, raw protocol.WeixinMessage) error {
	parsed := ParseInbound(raw)
	from := parsed.FromUserID
	now := s.opts.Now()
	if from != "" {
		if err := s.opts.Store.TouchInbound(s.opts.AccountID, from, parsed.ContextToken, now); err != nil {
			s.log.Error("touch inbound", "err", err, "from", from)
		}
	}

	msg := InboundMessage{
		AccountID:    s.opts.AccountID,
		FromUserID:   from,
		ContextToken: parsed.ContextToken,
		Text:         parsed.Text,
		MessageID:    parsed.MessageID,
		CreateTimeMs: parsed.CreateTimeMs,
		Quote:        parsed.Quote,
		Items:        parsed.Items,
		Raw:          raw,
		ReceivedAt:   now,
	}

	// download media items (failure does not drop message)
	codec := s.opts.SilkCodec
	if codec == nil {
		codec = media.DefaultSilkCodec
	}
	deps := media.DownloadDeps{
		CDN:   s.cdn,
		Store: s.media,
		SilkToWAV: func(silk []byte) ([]byte, error) {
			return media.SilkToWAV(silk, codec)
		},
	}
	for _, it := range raw.ItemList {
		lm, err := media.DownloadItem(ctx, it, deps)
		if err != nil {
			s.opts.OnMediaError(msg, err)
			continue
		}
		if lm != nil {
			msg.Media = append(msg.Media, *lm)
		}
	}

	if s.opts.Handler == nil {
		return nil
	}
	if err := s.opts.Handler(ctx, msg); err != nil {
		s.opts.OnHandlerError(msg, err)
		return err
	}
	return nil
}

func (s *Session) pauseFor(d time.Duration) {
	s.mu.Lock()
	s.paused = s.opts.Now().Add(d)
	s.mu.Unlock()
}

func (s *Session) pauseRemaining() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paused.IsZero() {
		return 0
	}
	d := s.paused.Sub(s.opts.Now())
	if d <= 0 {
		s.paused = time.Time{}
		return 0
	}
	return d
}

// ContextToken returns the stored token for a peer (empty if unknown).
// Uses unified resolution (legacy map or peer-state).
func (s *Session) ContextToken(userID string) string {
	return s.opts.Store.ResolveContextToken(s.opts.AccountID, userID)
}

// Peer returns durable peer state.
func (s *Session) Peer(userID string) (state.PeerState, error) {
	return s.opts.Store.GetPeer(s.opts.AccountID, userID)
}

// --- outbound ---

// SendTextOptions controls SendText behavior.
type SendTextOptions struct {
	SkipMarkdownFilter bool
}

// SendText sends a BOT text message with default markdown filter and chunking.
func (s *Session) SendText(ctx context.Context, toUserID, text string) error {
	return s.SendTextOpts(ctx, toUserID, text, SendTextOptions{})
}

// SendTextOpts is SendText with options.
func (s *Session) SendTextOpts(ctx context.Context, toUserID, text string, opts SendTextOptions) error {
	if toUserID == "" {
		return errors.New("session: toUserID required")
	}
	if !opts.SkipMarkdownFilter {
		text = markdown.Filter(text)
	}
	if text == "" {
		return nil
	}
	chunks := markdown.ChunkByRunes(text, s.opts.TextChunkLimit)
	if len(chunks) == 0 {
		return nil
	}
	// Reserve all chunks up front so multi-chunk text is never partially delivered
	// at the quota boundary, and concurrent Send* cannot overshoot.
	token, err := s.reserve(toUserID, len(chunks))
	if err != nil {
		return err
	}
	remaining := len(chunks)
	for _, chunk := range chunks {
		item := protocol.MessageItem{Type: protocol.ItemTypeText, TextItem: &protocol.TextItem{Text: chunk}}
		if err := s.sendItemReserved(ctx, toUserID, token, item); err != nil {
			return s.releaseOutbound(toUserID, remaining, err)
		}
		remaining--
	}
	return nil
}

// SendItem sends an arbitrary already-built MessageItem through policy + sendmessage.
func (s *Session) SendItem(ctx context.Context, toUserID string, item protocol.MessageItem) error {
	if toUserID == "" {
		return errors.New("session: toUserID required")
	}
	token, err := s.reserve(toUserID, 1)
	if err != nil {
		return err
	}
	if err := s.sendItemReserved(ctx, toUserID, token, item); err != nil {
		return s.releaseOutbound(toUserID, 1, err)
	}
	return nil
}

// SendToolStart sends TOOL_CALL_START (type 11).
func (s *Session) SendToolStart(ctx context.Context, toUserID, toolName, toolCallID string) error {
	return s.SendItem(ctx, toUserID, protocol.MessageItem{
		Type:         protocol.ItemTypeToolCallStart,
		IsCompleted:  false,
		CreateTimeMs: s.opts.Now().UnixMilli(),
		ToolCallStartItem: &protocol.ToolCallStartItem{
			ToolName:   toolName,
			ToolCallID: toolCallID,
		},
	})
}

// SendToolResult sends TOOL_CALL_RESULT (type 12).
func (s *Session) SendToolResult(ctx context.Context, toUserID, toolName, toolCallID, status string) error {
	return s.SendItem(ctx, toUserID, protocol.MessageItem{
		Type:         protocol.ItemTypeToolCallResult,
		IsCompleted:  true,
		CreateTimeMs: s.opts.Now().UnixMilli(),
		ToolCallResultItem: &protocol.ToolCallResultItem{
			ToolName:   toolName,
			ToolCallID: toolCallID,
			Status:     status,
		},
	})
}

// SendImageFile uploads a local image and sends IMAGE item (optional caption).
func (s *Session) SendImageFile(ctx context.Context, toUserID, filePath, caption string) error {
	return s.sendUploadedMedia(ctx, toUserID, filePath, caption, protocol.UploadMediaImage, func(up *media.Uploaded) protocol.MessageItem {
		aesB64 := aesKeyHexToBase64(up.AESKeyHex)
		return protocol.MessageItem{
			Type: protocol.ItemTypeImage,
			ImageItem: &protocol.ImageItem{
				Media: &protocol.CDNMedia{
					EncryptQueryParam: up.DownloadEncryptedQueryParam,
					AESKey:            aesB64,
					EncryptType:       1,
				},
				AESKey:  up.AESKeyHex,
				HDSize:  up.FileSizeCiphertext,
				MidSize: up.FileSizeCiphertext,
			},
		}
	})
}

// SendVideoFile uploads and sends a video.
func (s *Session) SendVideoFile(ctx context.Context, toUserID, filePath, caption string) error {
	return s.sendUploadedMedia(ctx, toUserID, filePath, caption, protocol.UploadMediaVideo, func(up *media.Uploaded) protocol.MessageItem {
		aesB64 := aesKeyHexToBase64(up.AESKeyHex)
		return protocol.MessageItem{
			Type: protocol.ItemTypeVideo,
			VideoItem: &protocol.VideoItem{
				Media: &protocol.CDNMedia{
					EncryptQueryParam: up.DownloadEncryptedQueryParam,
					AESKey:            aesB64,
					EncryptType:       1,
				},
				VideoSize: up.FileSize,
			},
		}
	})
}

// SendFileAttachment uploads a non-media file and sends FILE item.
func (s *Session) SendFileAttachment(ctx context.Context, toUserID, filePath string) error {
	return s.sendUploadedMedia(ctx, toUserID, filePath, "", protocol.UploadMediaFile, func(up *media.Uploaded) protocol.MessageItem {
		aesB64 := aesKeyHexToBase64(up.AESKeyHex)
		name := filepath.Base(filePath)
		return protocol.MessageItem{
			Type: protocol.ItemTypeFile,
			FileItem: &protocol.FileItem{
				Media: &protocol.CDNMedia{
					EncryptQueryParam: up.DownloadEncryptedQueryParam,
					AESKey:            aesB64,
					EncryptType:       1,
				},
				FileName: name,
				Len:      fmt.Sprintf("%d", up.FileSize),
			},
		}
	})
}

// SendVoice sends a local voice file. Accepts .silk (pass-through) or WAV/PCM
// encoded via SilkCodec. Returns error if input cannot be encoded.
func (s *Session) SendVoice(ctx context.Context, toUserID, filePath string) error {
	uploadPath, mediaType, build, cleanup, err := s.prepareMediaUpload(filePath)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return err
	}
	if mediaType != protocol.UploadMediaVoice {
		return fmt.Errorf("session: path is not voice media: %s", filePath)
	}
	return s.sendUploadedMedia(ctx, toUserID, uploadPath, "", mediaType, build)
}

func estimatePlaytimeMs(silk []byte) int {
	// rough: without decoder use size heuristic (~2.5KB/s typical)
	if len(silk) < 100 {
		return 1000
	}
	ms := len(silk) * 1000 / 2500
	if ms < 500 {
		ms = 500
	}
	return ms
}

// SendMedia routes by extension/MIME to image/video/voice/file.
// Caption + media share one outbound reservation (no partial send at quota edge).
// Voice paths are SILK-encoded the same way as SendVoice before upload.
func (s *Session) SendMedia(ctx context.Context, toUserID, path, caption string) error {
	if toUserID == "" {
		return errors.New("session: toUserID required")
	}
	uploadPath, mediaType, build, cleanup, err := s.prepareMediaUpload(path)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return err
	}
	return s.sendUploadedMedia(ctx, toUserID, uploadPath, caption, mediaType, build)
}

// SendMediaURL downloads url to a temp file then sends as media (optional caption).
// Policy reservation is held across fetch + upload + send (no TOCTOU release/re-reserve).
// Only http/https URLs are allowed; private/link-local/metadata hosts are blocked.
func (s *Session) SendMediaURL(ctx context.Context, toUserID, rawURL, caption string) error {
	if toUserID == "" {
		return errors.New("session: toUserID required")
	}
	// SSRF validation before any network or quota mutation.
	if err := s.validatePublicMediaURL(rawURL); err != nil {
		return err
	}

	captionChunks := s.captionChunks(caption)
	// slots = caption text messages + one media message
	slots := 1 + len(captionChunks)
	token, err := s.reserve(toUserID, slots)
	if err != nil {
		return err
	}
	remaining := slots

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return s.releaseOutbound(toUserID, remaining, err)
	}
	client := &http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("session: too many redirects")
			}
			return s.validatePublicMediaURL(req.URL.String())
		},
	}
	if s.opts.HTTP != nil {
		client.Transport = roundTripperFromDoer(s.opts.HTTP)
	}
	res, err := client.Do(req)
	if err != nil {
		return s.releaseOutbound(toUserID, remaining, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return s.releaseOutbound(toUserID, remaining, fmt.Errorf("session: media url status %d", res.StatusCode))
	}
	lim := s.opts.MaxMediaURLBytes
	data, err := io.ReadAll(io.LimitReader(res.Body, lim+1))
	if err != nil {
		return s.releaseOutbound(toUserID, remaining, err)
	}
	if int64(len(data)) > lim {
		return s.releaseOutbound(toUserID, remaining, fmt.Errorf("session: media url exceeds MaxMediaURLBytes %d", lim))
	}
	ext := media.ExtFromMIME(res.Header.Get("Content-Type"))
	if ext == ".bin" {
		ext = filepath.Ext(strings.Split(rawURL, "?")[0])
		if ext == "" {
			ext = ".bin"
		}
	}
	tmp, err := os.CreateTemp("", "weixinbot-url-*"+ext)
	if err != nil {
		return s.releaseOutbound(toUserID, remaining, err)
	}
	path := tmp.Name()
	defer os.Remove(path)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return s.releaseOutbound(toUserID, remaining, err)
	}
	if err := tmp.Close(); err != nil {
		return s.releaseOutbound(toUserID, remaining, err)
	}

	// Deliver caption + media using the held reservation (no second reserve).
	return s.deliverLocalWithReservation(ctx, toUserID, token, &remaining, path, captionChunks)
}

type rtDoer struct{ d ilink.Doer }

func (r rtDoer) RoundTrip(req *http.Request) (*http.Response, error) { return r.d.Do(req) }

func roundTripperFromDoer(d ilink.Doer) http.RoundTripper {
	if rt, ok := d.(http.RoundTripper); ok {
		return rt
	}
	// http.Client is a Doer
	if c, ok := d.(*http.Client); ok && c.Transport != nil {
		return c.Transport
	}
	return rtDoer{d: d}
}

func (s *Session) sendUploadedMedia(ctx context.Context, toUserID, filePath, caption string, mediaType int, build func(*media.Uploaded) protocol.MessageItem) error {
	if toUserID == "" {
		return errors.New("session: toUserID required")
	}
	chunks := s.captionChunks(caption)
	slots := 1 + len(chunks)
	token, err := s.reserve(toUserID, slots)
	if err != nil {
		return err
	}
	remaining := slots
	for _, c := range chunks {
		item := protocol.MessageItem{Type: protocol.ItemTypeText, TextItem: &protocol.TextItem{Text: c}}
		if err := s.sendItemReserved(ctx, toUserID, token, item); err != nil {
			return s.releaseOutbound(toUserID, remaining, err)
		}
		remaining--
	}
	return s.uploadAndSendReserved(ctx, toUserID, token, &remaining, filePath, mediaType, build)
}

// deliverLocalWithReservation sends optional pre-chunked caption + media for a local path,
// consuming slots from *remaining under an existing reservation.
// Voice files are SILK-encoded (same as SendVoice) before CDN upload.
func (s *Session) deliverLocalWithReservation(ctx context.Context, toUserID, token string, remaining *int, filePath string, captionChunks []string) error {
	for _, c := range captionChunks {
		item := protocol.MessageItem{Type: protocol.ItemTypeText, TextItem: &protocol.TextItem{Text: c}}
		if err := s.sendItemReserved(ctx, toUserID, token, item); err != nil {
			return s.releaseOutbound(toUserID, *remaining, err)
		}
		*remaining--
	}
	uploadPath, mediaType, build, cleanup, err := s.prepareMediaUpload(filePath)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return s.releaseOutbound(toUserID, *remaining, err)
	}
	return s.uploadAndSendReserved(ctx, toUserID, token, remaining, uploadPath, mediaType, build)
}

// prepareMediaUpload maps a local path to upload path, media type, and item builder.
// For voice, encodes WAV/PCM→SILK (or pass-through silk) into a temp file (cleanup required).
func (s *Session) prepareMediaUpload(filePath string) (
	uploadPath string,
	mediaType int,
	build func(*media.Uploaded) protocol.MessageItem,
	cleanup func(),
	err error,
) {
	switch media.DetectMediaKind(filePath) {
	case "image":
		return filePath, protocol.UploadMediaImage, func(up *media.Uploaded) protocol.MessageItem {
			aesB64 := aesKeyHexToBase64(up.AESKeyHex)
			return protocol.MessageItem{
				Type: protocol.ItemTypeImage,
				ImageItem: &protocol.ImageItem{
					Media: &protocol.CDNMedia{
						EncryptQueryParam: up.DownloadEncryptedQueryParam,
						AESKey:            aesB64,
						EncryptType:       1,
					},
					AESKey:  up.AESKeyHex,
					HDSize:  up.FileSizeCiphertext,
					MidSize: up.FileSizeCiphertext,
				},
			}
		}, nil, nil
	case "video":
		return filePath, protocol.UploadMediaVideo, func(up *media.Uploaded) protocol.MessageItem {
			aesB64 := aesKeyHexToBase64(up.AESKeyHex)
			return protocol.MessageItem{
				Type: protocol.ItemTypeVideo,
				VideoItem: &protocol.VideoItem{
					Media: &protocol.CDNMedia{
						EncryptQueryParam: up.DownloadEncryptedQueryParam,
						AESKey:            aesB64,
						EncryptType:       1,
					},
					VideoSize: up.FileSize,
				},
			}
		}, nil, nil
	case "voice":
		silkPath, playtime, clean, encErr := s.encodeVoiceToTempSilk(filePath)
		if encErr != nil {
			return "", 0, nil, clean, encErr
		}
		return silkPath, protocol.UploadMediaVoice, func(up *media.Uploaded) protocol.MessageItem {
			aesB64 := aesKeyHexToBase64(up.AESKeyHex)
			return protocol.MessageItem{
				Type: protocol.ItemTypeVoice,
				VoiceItem: &protocol.VoiceItem{
					Media: &protocol.CDNMedia{
						EncryptQueryParam: up.DownloadEncryptedQueryParam,
						AESKey:            aesB64,
						EncryptType:       1,
					},
					EncodeType:    1,
					BitsPerSample: 16,
					SampleRate:    media.SILKSampleRate,
					Playtime:      playtime,
				},
			}
		}, clean, nil
	default:
		return filePath, protocol.UploadMediaFile, func(up *media.Uploaded) protocol.MessageItem {
			aesB64 := aesKeyHexToBase64(up.AESKeyHex)
			return protocol.MessageItem{
				Type: protocol.ItemTypeFile,
				FileItem: &protocol.FileItem{
					Media: &protocol.CDNMedia{
						EncryptQueryParam: up.DownloadEncryptedQueryParam,
						AESKey:            aesB64,
						EncryptType:       1,
					},
					FileName: filepath.Base(filePath),
					Len:      fmt.Sprintf("%d", up.FileSize),
				},
			}
		}, nil, nil
	}
}

// encodeVoiceToTempSilk converts local audio to Tencent SILK for upload (same rules as SendVoice).
// cleanup removes the temp file when non-nil (always call when non-nil, even on error after create).
func (s *Session) encodeVoiceToTempSilk(filePath string) (tmpPath string, playtime int, cleanup func(), err error) {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return "", 0, nil, err
	}
	codec := s.opts.SilkCodec
	if codec == nil {
		codec = media.DefaultSilkCodec
	}
	silkData, err := media.WAVOrPCMToSilk(raw, codec)
	if err != nil {
		return "", 0, nil, fmt.Errorf("session: voice encode: %w", err)
	}
	tmp, err := os.CreateTemp("", "weixinbot-voice-*.silk")
	if err != nil {
		return "", 0, nil, err
	}
	tmpPath = tmp.Name()
	cleanup = func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(silkData); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", 0, nil, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", 0, nil, err
	}
	return tmpPath, estimatePlaytimeMs(silkData), cleanup, nil
}

func (s *Session) uploadAndSendReserved(ctx context.Context, toUserID, token string, remaining *int, filePath string, mediaType int, build func(*media.Uploaded) protocol.MessageItem) error {
	up, err := media.UploadFile(ctx, s.opts.Client, s.cdn, filePath, toUserID, mediaType)
	if err != nil {
		return s.releaseOutbound(toUserID, *remaining, err)
	}
	if err := s.sendItemReserved(ctx, toUserID, token, build(up)); err != nil {
		return s.releaseOutbound(toUserID, *remaining, err)
	}
	*remaining--
	return nil
}

func (s *Session) captionChunks(caption string) []string {
	if caption == "" {
		return nil
	}
	text := markdown.Filter(caption)
	if text == "" {
		return nil
	}
	return markdown.ChunkByRunes(text, s.opts.TextChunkLimit)
}

// reserve atomically checks policy and increments outbound count by n.
func (s *Session) reserve(toUserID string, n int) (contextToken string, err error) {
	res, err := s.opts.Store.TryReserveOutbound(s.opts.AccountID, toUserID, s.opts.Policy.reserveOpts(n))
	if err != nil {
		return "", mapReserveErr(err)
	}
	return res.ContextToken, nil
}

// releaseOutbound releases n reserved slots and returns cause, or a combined error if release fails.
func (s *Session) releaseOutbound(toUserID string, n int, cause error) error {
	if n <= 0 {
		return cause
	}
	if err := s.opts.Store.ReleaseOutbound(s.opts.AccountID, toUserID, n); err != nil {
		if cause != nil {
			return fmt.Errorf("%w; release outbound: %v", cause, err)
		}
		return fmt.Errorf("session: release outbound: %w", err)
	}
	return cause
}

// sendItemReserved sends with a pre-resolved context token; does not change outbound count.
func (s *Session) sendItemReserved(ctx context.Context, toUserID, token string, item protocol.MessageItem) error {
	msg := &protocol.WeixinMessage{
		ToUserID:     toUserID,
		ClientID:     newClientID(),
		MessageType:  protocol.MessageTypeBot,
		MessageState: protocol.MessageStateFinish,
		ContextToken: token,
		ItemList:     []protocol.MessageItem{item},
	}
	return s.sendWithRetry(ctx, msg)
}

func (s *Session) sendWithRetry(ctx context.Context, msg *protocol.WeixinMessage) error {
	attempts := s.opts.RetryRateLimit + 1
	var err error
	backoff := s.opts.RateLimitBackoff
	for i := 0; i < attempts; i++ {
		err = s.opts.Client.SendMessage(ctx, msg)
		if err == nil {
			return nil
		}
		// never retry policy errors (not returned here) or non-rate-limit
		if !ilink.IsRateLimited(err) || i == attempts-1 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return err
}

// --- typing ---

// StartTyping begins typing indicator (status=1). No-op if ticket unavailable.
func (s *Session) StartTyping(ctx context.Context, userID string) error {
	token := s.ContextToken(userID)
	ticket := s.cfg.TypingTicket(ctx, userID, token)
	if ticket == "" {
		return nil
	}
	return s.opts.Client.SendTyping(ctx, userID, ticket, protocol.TypingStatusTyping)
}

// StopTyping cancels typing indicator (status=2).
func (s *Session) StopTyping(ctx context.Context, userID string) error {
	token := s.ContextToken(userID)
	ticket := s.cfg.TypingTicket(ctx, userID, token)
	if ticket == "" {
		return nil
	}
	return s.opts.Client.SendTyping(ctx, userID, ticket, protocol.TypingStatusCancel)
}

// WithTyping runs fn while sending typing keepalive; stops typing when done.
func (s *Session) WithTyping(ctx context.Context, userID string, fn func(context.Context) error) error {
	tctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.StartTyping(tctx, userID)
		ticker := time.NewTicker(s.opts.TypingKeepalive)
		defer ticker.Stop()
		for {
			select {
			case <-tctx.Done():
				return
			case <-ticker.C:
				_ = s.StartTyping(tctx, userID)
			}
		}
	}()
	err := fn(ctx)
	cancel()
	<-done
	_ = s.StopTyping(context.Background(), userID)
	return err
}

func aesKeyHexToBase64(hexKey string) string {
	b, err := hex.DecodeString(hexKey)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}

// validatePublicMediaURL enforces http(s) and rejects private/link-local/metadata hosts.
// DNS lookup failures fail closed unless MediaURLSkipDNSCheck is set (tests only).
func (s *Session) validatePublicMediaURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("session: bad media url: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("session: media url scheme %q not allowed", u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("session: media url must not contain userinfo")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("session: media url missing host")
	}
	lh := strings.ToLower(host)
	if lh == "localhost" || strings.HasSuffix(lh, ".localhost") ||
		lh == "metadata.google.internal" || strings.HasSuffix(lh, ".internal") {
		return fmt.Errorf("session: media url host %q blocked", host)
	}
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("session: media url host IP blocked")
		}
		return nil
	}
	if s.opts.MediaURLSkipDNSCheck {
		// Tests with fake hosts + injected RoundTripper only.
		return nil
	}
	addrs, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("session: media url host lookup failed: %w", err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("session: media url host lookup returned no addresses")
	}
	for _, ip := range addrs {
		if isBlockedIP(ip) {
			return fmt.Errorf("session: media url resolves to blocked IP %s", ip)
		}
	}
	return nil
}

func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		// 169.254.0.0/16 link-local / cloud metadata
		if ip4[0] == 169 && ip4[1] == 254 {
			return true
		}
		// 0.0.0.0/8
		if ip4[0] == 0 {
			return true
		}
	} else {
		// IPv6 unique local fc00::/7, site-local deprecated, etc. covered partly by IsPrivate.
		if ip.IsPrivate() {
			return true
		}
	}
	return false
}

func newClientID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "weixinbot-" + hex.EncodeToString(b[:])
}

// Open is a convenience constructor: load account from store and build Client+Session.
func Open(store *state.Store, accountID string, httpDoer ilink.Doer, handler Handler) (*Session, error) {
	acc, err := store.LoadAccount(accountID)
	if err != nil {
		return nil, err
	}
	base := acc.BaseURL
	if strings.TrimSpace(base) == "" {
		base = ilink.DefaultBaseURL
	}
	cdn := acc.CDNBase
	if cdn == "" {
		cdn = ilink.DefaultCDNBaseURL
	}
	client := ilink.NewClient(ilink.Config{
		BaseURL: base,
		Token:   acc.Token,
		HTTP:    httpDoer,
	})
	return New(Options{
		AccountID:  accountID,
		Client:     client,
		Store:      store,
		CDNBaseURL: cdn,
		HTTP:       httpDoer,
		Handler:    handler,
	})
}
