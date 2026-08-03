// Package auth implements iLink bot QR login (no OpenClaw dependency).
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/fun7257/weixinbot/ilink"
	"github.com/fun7257/weixinbot/state"
)

// DefaultBotType is the iLink bot_type for get_bot_qrcode.
const DefaultBotType = "3"

// FixedLoginBaseURL is the production QR login host.
const FixedLoginBaseURL = "https://ilinkai.weixin.qq.com"

// Paths for QR CGI (relative).
const (
	PathGetBotQRCode   = "ilink/bot/get_bot_qrcode"
	PathGetQRCodeStatus = "ilink/bot/get_qrcode_status"
)

// Status values from get_qrcode_status.
const (
	StatusWait              = "wait"
	StatusScanned           = "scaned"
	StatusConfirmed         = "confirmed"
	StatusExpired           = "expired"
	StatusNeedVerifyCode    = "need_verifycode"
	StatusVerifyCodeBlocked = "verify_code_blocked"
	StatusScannedRedirect   = "scaned_but_redirect"
	StatusBindedRedirect    = "binded_redirect"
)

// MaxQRRefresh is max QR refreshes after expire/block.
const MaxQRRefresh = 3

// VerifyCodeFunc obtains a pairing code from the user (e.g. stdin).
type VerifyCodeFunc func(ctx context.Context, prompt string) (string, error)

// Options for QR login.
type Options struct {
	// BaseURL defaults to FixedLoginBaseURL.
	BaseURL string
	// BotType defaults to DefaultBotType.
	BotType string
	// HTTP client boundary for tests.
	HTTP ilink.Doer
	// LocalTokens optional list of existing bot tokens (local_token_list).
	LocalTokens []string
	// VerifyCode is required for need_verifycode flows.
	VerifyCode VerifyCodeFunc
	// MaxQRRefresh overrides MaxQRRefresh (tests may set 1).
	MaxQRRefresh int
	// PollInterval between status polls after non-terminal status (default 0 in tests via direct Wait).
	PollInterval time.Duration
	// StatusTimeout client timeout for each status long-poll (default 35s).
	StatusTimeout time.Duration
	// AllowAnyHost disables redirect_host / baseurl allowlist (tests only).
	AllowAnyHost bool
	// AllowedHostSuffixes optional extra suffixes; default weixin.qq.com / qq.com.
	AllowedHostSuffixes []string
}

// QRStart is returned by StartQR.
type QRStart struct {
	QRCodeURL  string
	SessionKey string
	RawQRCode  string
}

// LoginResult is a successful or already-connected login outcome.
type LoginResult struct {
	Connected        bool
	AlreadyConnected bool
	BotToken         string
	AccountID        string
	BaseURL          string
	UserID           string
	Message          string
}

type activeLogin struct {
	sessionKey       string
	qrcode           string
	qrcodeURL        string
	startedAt        time.Time
	currentBaseURL   string
	pendingVerifyCode string
}

var (
	activeMu     sync.Mutex
	activeLogins = map[string]*activeLogin{}
)

// StartQR fetches a QR code and returns URL + session key.
func StartQR(ctx context.Context, sessionKey string, opts Options) (*QRStart, error) {
	opts = opts.withDefaults()
	if sessionKey == "" {
		sessionKey = fmt.Sprintf("sess-%d", time.Now().UnixNano())
	}
	qr, img, err := fetchQRCode(ctx, opts)
	if err != nil {
		return nil, err
	}
	activeMu.Lock()
	activeLogins[sessionKey] = &activeLogin{
		sessionKey:     sessionKey,
		qrcode:         qr,
		qrcodeURL:      img,
		startedAt:      time.Now(),
		currentBaseURL: opts.BaseURL,
	}
	activeMu.Unlock()
	return &QRStart{QRCodeURL: img, SessionKey: sessionKey, RawQRCode: qr}, nil
}

// WaitLogin polls QR status until confirmed, failed, or ctx done.
func WaitLogin(ctx context.Context, sessionKey string, opts Options) (*LoginResult, error) {
	opts = opts.withDefaults()
	activeMu.Lock()
	login := activeLogins[sessionKey]
	activeMu.Unlock()
	if login == nil {
		return &LoginResult{Message: "当前没有进行中的登录，请先发起登录。"}, fmt.Errorf("auth: no active login")
	}

	qrRefresh := 0
	for {
		if err := ctx.Err(); err != nil {
			return &LoginResult{Message: "登录超时，请重试。"}, err
		}
		status, err := pollStatus(ctx, login.currentBaseURL, login.qrcode, login.pendingVerifyCode, opts)
		if err != nil {
			// treat network as wait
			if opts.PollInterval > 0 {
				select {
				case <-ctx.Done():
					return &LoginResult{Message: "登录超时，请重试。"}, ctx.Err()
				case <-time.After(opts.PollInterval):
				}
			}
			continue
		}
		switch status.Status {
		case StatusWait, StatusScanned:
			if login.pendingVerifyCode != "" && status.Status == StatusScanned {
				login.pendingVerifyCode = ""
			}
		case StatusNeedVerifyCode:
			if opts.VerifyCode == nil {
				return &LoginResult{Message: "需要验证码但未提供 VerifyCodeFunc"}, fmt.Errorf("auth: need verify code")
			}
			prompt := "输入手机微信显示的数字，以继续连接："
			if login.pendingVerifyCode != "" {
				prompt = "❌ 你输入的数字不匹配，请重新输入："
			}
			code, err := opts.VerifyCode(ctx, prompt)
			if err != nil {
				return nil, err
			}
			login.pendingVerifyCode = code
			continue
		case StatusExpired, StatusVerifyCodeBlocked:
			login.pendingVerifyCode = ""
			qrRefresh++
			if qrRefresh > opts.MaxQRRefresh {
				activeMu.Lock()
				delete(activeLogins, sessionKey)
				activeMu.Unlock()
				return &LoginResult{Message: "二维码多次失效，连接流程已停止。请稍后再试。"}, fmt.Errorf("auth: qr refresh limit")
			}
			qr, img, err := fetchQRCode(ctx, opts)
			if err != nil {
				return nil, err
			}
			login.qrcode = qr
			login.qrcodeURL = img
			login.startedAt = time.Now()
			login.currentBaseURL = opts.BaseURL
		case StatusScannedRedirect:
			if status.RedirectHost != "" {
				base, err := normalizeAllowedBaseURL("https://"+status.RedirectHost, opts)
				if err != nil {
					// ignore invalid redirect_host; keep current poll host
					break
				}
				login.currentBaseURL = base
			}
		case StatusBindedRedirect:
			activeMu.Lock()
			delete(activeLogins, sessionKey)
			activeMu.Unlock()
			return &LoginResult{
				Connected:        false,
				AlreadyConnected: true,
				Message:          "已连接过，无需重复连接。",
			}, nil
		case StatusConfirmed:
			if status.ILinkBotID == "" {
				return &LoginResult{Message: "登录失败：服务器未返回 ilink_bot_id。"}, fmt.Errorf("auth: missing ilink_bot_id")
			}
			baseURL := status.BaseURL
			if baseURL != "" {
				if b, err := normalizeAllowedBaseURL(baseURL, opts); err == nil {
					baseURL = b
				} else if !opts.AllowAnyHost {
					// reject attacker-controlled baseurl; fall back to poll host / default
					baseURL = login.currentBaseURL
					if baseURL == "" {
						baseURL = FixedLoginBaseURL
					}
				}
			}
			activeMu.Lock()
			delete(activeLogins, sessionKey)
			activeMu.Unlock()
			return &LoginResult{
				Connected: true,
				BotToken:  status.BotToken,
				AccountID: status.ILinkBotID,
				BaseURL:   baseURL,
				UserID:    status.ILinkUserID,
				Message:   "已连接到微信。",
			}, nil
		}
		if opts.PollInterval > 0 {
			select {
			case <-ctx.Done():
				return &LoginResult{Message: "登录超时，请重试。"}, ctx.Err()
			case <-time.After(opts.PollInterval):
			}
		}
	}
}

// CompleteLogin writes LoginResult into the state store and registers the account.
func CompleteLogin(store *state.Store, result *LoginResult) error {
	if store == nil || result == nil {
		return fmt.Errorf("auth: nil store or result")
	}
	if result.AlreadyConnected {
		return nil
	}
	if !result.Connected || result.BotToken == "" || result.AccountID == "" {
		return fmt.Errorf("auth: incomplete login result")
	}
	if err := store.RegisterAccountID(result.AccountID); err != nil {
		return err
	}
	base := result.BaseURL
	if base == "" {
		base = ilink.DefaultBaseURL
	} else {
		// Validate persisted baseurl (https + weixin host) unless already sanitized.
		if b, err := normalizeAllowedBaseURL(base, Options{}); err == nil {
			base = b
		} else {
			return fmt.Errorf("auth: invalid baseurl: %w", err)
		}
	}
	if err := store.SaveAccount(result.AccountID, state.Account{
		Token:   result.BotToken,
		BaseURL: base,
		UserID:  result.UserID,
	}); err != nil {
		return err
	}
	if result.UserID != "" {
		_ = store.ClearStaleAccountsForUserID(result.AccountID, result.UserID)
	}
	return nil
}

// normalizeAllowedBaseURL requires https (or http only when AllowAnyHost) and
// host under weixin.qq.com / qq.com (or AllowedHostSuffixes).
func normalizeAllowedBaseURL(raw string, opts Options) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("auth: empty base url")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.User != nil {
		return "", fmt.Errorf("auth: base url must not contain userinfo")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", fmt.Errorf("auth: base url missing host")
	}
	if !opts.AllowAnyHost {
		scheme := strings.ToLower(u.Scheme)
		if scheme != "https" {
			return "", fmt.Errorf("auth: base url must be https, got %q", u.Scheme)
		}
		if net.ParseIP(host) != nil {
			return "", fmt.Errorf("auth: base url must not be IP literal")
		}
		if !hostAllowed(host, opts.AllowedHostSuffixes) {
			return "", fmt.Errorf("auth: host %q not allowlisted", host)
		}
	}
	// Rebuild without path/query/fragment noise for base origin.
	out := u.Scheme + "://" + u.Host
	return strings.TrimRight(out, "/"), nil
}

func hostAllowed(host string, extra []string) bool {
	defaults := []string{"weixin.qq.com", "qq.com"}
	check := append(append([]string{}, defaults...), extra...)
	for _, suf := range check {
		suf = strings.ToLower(strings.TrimSpace(suf))
		if suf == "" {
			continue
		}
		if host == suf || strings.HasSuffix(host, "."+suf) {
			return true
		}
	}
	return false
}

// StdinVerifyCode returns a VerifyCodeFunc that reads a line from r (usually os.Stdin).
func StdinVerifyCode(r io.Reader, w io.Writer) VerifyCodeFunc {
	return func(ctx context.Context, prompt string) (string, error) {
		if w != nil {
			fmt.Fprint(w, prompt)
		}
		// simple line read
		buf := make([]byte, 0, 64)
		tmp := make([]byte, 1)
		for {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			n, err := r.Read(tmp)
			if n > 0 {
				if tmp[0] == '\n' {
					return strings.TrimSpace(string(buf)), nil
				}
				if tmp[0] != '\r' {
					buf = append(buf, tmp[0])
				}
			}
			if err != nil {
				if len(buf) > 0 {
					return strings.TrimSpace(string(buf)), nil
				}
				return "", err
			}
		}
	}
}

type qrCodeResponse struct {
	QRCode          string `json:"qrcode"`
	QRCodeImgContent string `json:"qrcode_img_content"`
}

type statusResponse struct {
	Status       string `json:"status"`
	BotToken     string `json:"bot_token"`
	ILinkBotID   string `json:"ilink_bot_id"`
	BaseURL      string `json:"baseurl"`
	ILinkUserID  string `json:"ilink_user_id"`
	RedirectHost string `json:"redirect_host"`
}

func (o Options) withDefaults() Options {
	if o.BaseURL == "" {
		o.BaseURL = FixedLoginBaseURL
	}
	o.BaseURL = strings.TrimRight(o.BaseURL, "/")
	if o.BotType == "" {
		o.BotType = DefaultBotType
	}
	if o.MaxQRRefresh <= 0 {
		o.MaxQRRefresh = MaxQRRefresh
	}
	if o.StatusTimeout <= 0 {
		o.StatusTimeout = 35 * time.Second
	}
	if o.HTTP == nil {
		o.HTTP = http.DefaultClient
	}
	return o
}

func fetchQRCode(ctx context.Context, opts Options) (qrcode, img string, err error) {
	u := opts.BaseURL + "/" + PathGetBotQRCode + "?bot_type=" + url.QueryEscape(opts.BotType)
	body, _ := json.Marshal(map[string]any{"local_token_list": opts.LocalTokens})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(string(body)))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := opts.HTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return "", "", err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", "", fmt.Errorf("auth: get_bot_qrcode http %d: %s", res.StatusCode, data)
	}
	var resp qrCodeResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", "", err
	}
	if resp.QRCode == "" {
		return "", "", fmt.Errorf("auth: empty qrcode")
	}
	return resp.QRCode, resp.QRCodeImgContent, nil
}

func pollStatus(ctx context.Context, base, qrcode, verify string, opts Options) (*statusResponse, error) {
	base = strings.TrimRight(base, "/")
	endpoint := PathGetQRCodeStatus + "?qrcode=" + url.QueryEscape(qrcode)
	if verify != "" {
		endpoint += "&verify_code=" + url.QueryEscape(verify)
	}
	u := base + "/" + endpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	// apply status timeout
	if opts.StatusTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.StatusTimeout)
		defer cancel()
		req = req.WithContext(ctx)
	}
	res, err := opts.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("auth: status http %d", res.StatusCode)
	}
	var resp statusResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ResetActiveLoginsForTest clears in-memory QR sessions (tests only).
func ResetActiveLoginsForTest() {
	activeMu.Lock()
	activeLogins = map[string]*activeLogin{}
	activeMu.Unlock()
}
