// Package ilink is the iLink Bot HTTP transport: shared headers and CGI helpers.
//
// Protocol model: client pull (getupdates long-poll) + client push (sendmessage).
// This package does not implement OpenClaw or any agent host.
package ilink

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fun7257/weixinbot/protocol"
)

// DefaultBaseURL is the production iLink host used when login does not override it.
const DefaultBaseURL = "https://ilinkai.weixin.qq.com"

// DefaultCDNBaseURL is the default C2C CDN base.
const DefaultCDNBaseURL = "https://novac2c.cdn.weixin.qq.com/c2c"

// Doer is the HTTP boundary used by the client (tests inject fakes here).
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Config configures an iLink API client for one bot credential set.
type Config struct {
	// BaseURL is the iLink origin, e.g. https://ilinkai.weixin.qq.com
	BaseURL string
	// Token is the bot bearer token from QR login (may be empty for unauthenticated login CGI).
	Token string
	// AppID maps to iLink-App-Id (package ilink_appid; TS default "bot").
	AppID string
	// ClientVersion is iLink-App-ClientVersion uint32 encoding (0x00MMNNPP).
	ClientVersion uint32
	// ChannelVersion is base_info.channel_version.
	ChannelVersion string
	// BotAgent is base_info.bot_agent (User-Agent style observability string).
	BotAgent string
	// RouteTag optional SKRouteTag header.
	RouteTag string
	// HTTP client; if nil, http.DefaultClient is used (prefer inject for tests).
	HTTP Doer
	// Timeout for non-long-poll requests.
	Timeout time.Duration
}

// Client talks to iLink Bot CGI endpoints.
type Client struct {
	cfg  Config
	http Doer
}

// NewClient returns a configured Client. BaseURL is normalized with a trailing slash.
func NewClient(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if !strings.HasSuffix(cfg.BaseURL, "/") {
		cfg.BaseURL += "/"
	}
	if cfg.AppID == "" {
		cfg.AppID = "bot"
	}
	if cfg.BotAgent == "" {
		cfg.BotAgent = "WeixinBot/go"
	}
	if cfg.ChannelVersion == "" {
		cfg.ChannelVersion = "0.1.0"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}
	d := cfg.HTTP
	if d == nil {
		d = &http.Client{Timeout: 0} // per-request context deadlines
	}
	return &Client{cfg: cfg, http: d}
}

// BaseURL returns the normalized base URL.
func (c *Client) BaseURL() string { return c.cfg.BaseURL }

// Token returns the bot token.
func (c *Client) Token() string { return c.cfg.Token }

// WithToken returns a shallow copy of the client using a different token.
func (c *Client) WithToken(token string) *Client {
	cp := *c
	cp.cfg.Token = token
	return &cp
}

func (c *Client) baseInfo() *protocol.BaseInfo {
	return &protocol.BaseInfo{
		ChannelVersion: c.cfg.ChannelVersion,
		BotAgent:       c.cfg.BotAgent,
	}
}

func randomWechatUIN() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	u := binary.BigEndian.Uint32(b[:])
	return base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(u), 10)))
}

func (c *Client) commonHeaders(withAuth bool) http.Header {
	h := make(http.Header)
	h.Set("iLink-App-Id", c.cfg.AppID)
	h.Set("iLink-App-ClientVersion", strconv.FormatUint(uint64(c.cfg.ClientVersion), 10))
	if c.cfg.RouteTag != "" {
		h.Set("SKRouteTag", c.cfg.RouteTag)
	}
	if withAuth {
		h.Set("Content-Type", "application/json")
		h.Set("AuthorizationType", protocol.AuthorizationTypeILinkBotToken)
		h.Set("X-WECHAT-UIN", randomWechatUIN())
		if strings.TrimSpace(c.cfg.Token) != "" {
			h.Set("Authorization", "Bearer "+strings.TrimSpace(c.cfg.Token))
		}
	}
	return h
}

// APIError is a non-zero ret/errcode from iLink JSON body or HTTP failure.
type APIError struct {
	Op      string
	Ret     int
	ErrCode int
	ErrMsg  string
	Status  int
}

// Error implements the error interface for APIError.
func (e *APIError) Error() string {
	if e.Status != 0 && e.Status != http.StatusOK {
		return fmt.Sprintf("%s: http %d %s", e.Op, e.Status, e.ErrMsg)
	}
	return fmt.Sprintf("%s: ret=%d errcode=%d errmsg=%s", e.Op, e.Ret, e.ErrCode, e.ErrMsg)
}

// RateLimitedRet is the iLink ret code for rate limiting (sendmessage).
const RateLimitedRet = -2

// IsStaleToken reports whether the error indicates errcode/ret -14.
func IsStaleToken(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) {
		return false
	}
	return ae.ErrCode == protocol.StaleTokenErrCode || ae.Ret == protocol.StaleTokenErrCode
}

// IsRateLimited reports whether the error indicates ret=-2 or a rate-limit errmsg.
func IsRateLimited(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) {
		return false
	}
	if ae.Ret == RateLimitedRet || ae.ErrCode == RateLimitedRet {
		return true
	}
	msg := strings.ToLower(ae.ErrMsg)
	return strings.Contains(msg, "rate limit") || strings.Contains(msg, "rate_limit") || strings.Contains(msg, "ratelimit")
}

// postJSON marshals body (any for CGI JSON shapes) and POSTs with bot headers.
func (c *Client) postJSON(ctx context.Context, op, path string, body any, timeout time.Duration) ([]byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%s: marshal: %w", op, err)
	}
	u, err := url.Parse(c.cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("%s: base url: %w", op, err)
	}
	ref, err := url.Parse(path)
	if err != nil {
		return nil, fmt.Errorf("%s: path: %w", op, err)
	}
	full := u.ResolveReference(ref).String()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, full, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: new request: %w", op, err)
	}
	for k, vv := range c.commonHeaders(true) {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}

	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
		req = req.WithContext(ctx)
	}

	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: do: %w", op, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: read body: %w", op, err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, &APIError{Op: op, Status: res.StatusCode, ErrMsg: string(data)}
	}
	return data, nil
}

// GetUpdates long-polls for inbound messages.
// On context deadline/cancel during long-poll, the error from http is returned as-is
// so the session loop can distinguish cancel vs empty retry.
func (c *Client) GetUpdates(ctx context.Context, getUpdatesBuf string, longPollTimeout time.Duration) (*protocol.GetUpdatesResp, error) {
	if longPollTimeout <= 0 {
		longPollTimeout = 35 * time.Second
	}
	// Client timeout slightly above server hold so we abort if the server never answers.
	body := protocol.GetUpdatesReq{
		GetUpdatesBuf: getUpdatesBuf,
		BaseInfo:      c.baseInfo(),
	}
	data, err := c.postJSON(ctx, "getUpdates", protocol.PathGetUpdates, body, longPollTimeout+2*time.Second)
	if err != nil {
		return nil, err
	}
	var resp protocol.GetUpdatesResp
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("getUpdates: decode: %w", err)
	}
	return &resp, nil
}

// SendMessage sends a single outbound WeixinMessage envelope.
//
// Production iLink often returns HTTP 200 with an empty body or "{}" on success
// (no ret field). Empty / whitespace bodies are treated as success, matching
// observed server behavior and sendTyping's empty-body handling.
func (c *Client) SendMessage(ctx context.Context, msg *protocol.WeixinMessage) error {
	body := protocol.SendMessageReq{Msg: msg, BaseInfo: c.baseInfo()}
	data, err := c.postJSON(ctx, "sendMessage", protocol.PathSendMessage, body, c.cfg.Timeout)
	if err != nil {
		return err
	}
	resp, err := decodeSendMessageResp(data)
	if err != nil {
		return err
	}
	if resp.Ret != 0 {
		return &APIError{Op: "sendMessage", Ret: resp.Ret, ErrMsg: resp.ErrMsg}
	}
	return nil
}

// decodeSendMessageResp parses sendmessage JSON. Empty body → success (zero resp).
func decodeSendMessageResp(data []byte) (protocol.SendMessageResp, error) {
	var resp protocol.SendMessageResp
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return resp, nil
	}
	if err := json.Unmarshal(trimmed, &resp); err != nil {
		snippet := string(trimmed)
		if len(snippet) > 200 {
			snippet = snippet[:200] + "…"
		}
		return resp, fmt.Errorf("sendMessage: decode: %w (body=%q)", err, snippet)
	}
	return resp, nil
}

// GetUploadURL requests CDN upload parameters for a file.
func (c *Client) GetUploadURL(ctx context.Context, req *protocol.GetUploadURLReq) (*protocol.GetUploadURLResp, error) {
	if req == nil {
		return nil, fmt.Errorf("getUploadUrl: nil request")
	}
	req.BaseInfo = c.baseInfo()
	data, err := c.postJSON(ctx, "getUploadUrl", protocol.PathGetUploadURL, req, c.cfg.Timeout)
	if err != nil {
		return nil, err
	}
	var resp protocol.GetUploadURLResp
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return &resp, nil
	}
	if err := json.Unmarshal(trimmed, &resp); err != nil {
		return nil, fmt.Errorf("getUploadUrl: decode: %w (body=%q)", err, clipBody(trimmed))
	}
	// getuploadurl does not always use ret field; leave as-is when absent.
	return &resp, nil
}

func clipBody(b []byte) string {
	s := string(b)
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

// GetConfig fetches typing_ticket (and related) for a user.
func (c *Client) GetConfig(ctx context.Context, ilinkUserID, contextToken string) (*protocol.GetConfigResp, error) {
	body := protocol.GetConfigReq{
		ILinkUserID:  ilinkUserID,
		ContextToken: contextToken,
		BaseInfo:     c.baseInfo(),
	}
	data, err := c.postJSON(ctx, "getConfig", protocol.PathGetConfig, body, 10*time.Second)
	if err != nil {
		return nil, err
	}
	var resp protocol.GetConfigResp
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return &resp, nil
	}
	if err := json.Unmarshal(trimmed, &resp); err != nil {
		return nil, fmt.Errorf("getConfig: decode: %w (body=%q)", err, clipBody(trimmed))
	}
	if resp.Ret != 0 {
		return &resp, &APIError{Op: "getConfig", Ret: resp.Ret, ErrMsg: resp.ErrMsg}
	}
	return &resp, nil
}

// SendTyping sends or cancels a typing indicator.
func (c *Client) SendTyping(ctx context.Context, ilinkUserID, typingTicket string, status int) error {
	body := protocol.SendTypingReq{
		ILinkUserID:  ilinkUserID,
		TypingTicket: typingTicket,
		Status:       status,
		BaseInfo:     c.baseInfo(),
	}
	data, err := c.postJSON(ctx, "sendTyping", protocol.PathSendTyping, body, 10*time.Second)
	if err != nil {
		return err
	}
	// typing CGI may return empty body on success; best-effort ret check
	var resp struct {
		Ret    int    `json:"ret"`
		ErrMsg string `json:"errmsg"`
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &resp); err == nil && resp.Ret != 0 {
			return &APIError{Op: "sendTyping", Ret: resp.Ret, ErrMsg: resp.ErrMsg}
		}
	}
	return nil
}

// NotifyStart notifies the server that this channel client is starting.
func (c *Client) NotifyStart(ctx context.Context) (*protocol.NotifyResp, error) {
	return c.notify(ctx, "notifyStart", protocol.PathNotifyStart)
}

// NotifyStop notifies the server that this channel client is stopping.
func (c *Client) NotifyStop(ctx context.Context) (*protocol.NotifyResp, error) {
	return c.notify(ctx, "notifyStop", protocol.PathNotifyStop)
}

func (c *Client) notify(ctx context.Context, op, path string) (*protocol.NotifyResp, error) {
	body := protocol.NotifyReq{BaseInfo: c.baseInfo()}
	data, err := c.postJSON(ctx, op, path, body, 10*time.Second)
	if err != nil {
		return nil, err
	}
	var resp protocol.NotifyResp
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("%s: decode: %w", op, err)
	}
	if resp.Ret != 0 {
		return &resp, &APIError{Op: op, Ret: resp.Ret, ErrMsg: resp.ErrMsg}
	}
	return &resp, nil
}

// BuildClientVersion encodes major.minor.patch as 0x00MMNNPP.
func BuildClientVersion(major, minor, patch int) uint32 {
	return uint32((major&0xff)<<16 | (minor&0xff)<<8 | (patch & 0xff))
}
