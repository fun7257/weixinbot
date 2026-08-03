package session_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tencent-weixin/weixinbot/ilink"
	"github.com/tencent-weixin/weixinbot/internal/testutil"
	"github.com/tencent-weixin/weixinbot/media"
	"github.com/tencent-weixin/weixinbot/protocol"
	"github.com/tencent-weixin/weixinbot/session"
	"github.com/tencent-weixin/weixinbot/state"
)

// withTestCDNFlags enables explicit test-only CDN/SSRF relaxations (never production defaults).
func withTestCDNFlags(o session.Options) session.Options {
	o.AllowAnyCDNFullURL = true
	o.MediaURLSkipDNSCheck = true
	if o.CDNBaseURL == "" {
		o.CDNBaseURL = "https://cdn-test.example/c2c"
	}
	return o
}

func setupAccount(t *testing.T, ft *testutil.FakeTransport) (*state.Store, *session.Session, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := state.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	const accountID = "bot-1"
	_ = st.RegisterAccountID(accountID)
	_ = st.SaveAccount(accountID, state.Account{Token: "test-bot-token", BaseURL: "https://ilink.test"})
	ms, err := media.NewStore(filepath.Join(dir, "media"))
	if err != nil {
		t.Fatal(err)
	}
	client := ilink.NewClient(ilink.Config{BaseURL: "https://ilink.test", Token: "test-bot-token", HTTP: ft.Client()})
	var sess *session.Session
	sess, err = session.New(withTestCDNFlags(session.Options{
		AccountID:       accountID,
		Client:          client,
		Store:           st,
		HTTP:            ft.Client(),
		MediaStore:      ms,
		LongPollTimeout: 50 * time.Millisecond,
		RetryDelay:      10 * time.Millisecond,
		StaleTokenPause: 30 * time.Millisecond,
		TypingKeepalive: 20 * time.Millisecond,
	}))
	if err != nil {
		t.Fatal(err)
	}
	return st, sess, accountID
}

func defaultCGI(ft *testutil.FakeTransport) {
	ft.OnContains(protocol.PathNotifyStart, testutil.JSONResponder(200, protocol.NotifyResp{Ret: 0}, nil))
	ft.OnContains(protocol.PathNotifyStop, testutil.JSONResponder(200, protocol.NotifyResp{Ret: 0}, nil))
	ft.OnContains(protocol.PathSendMessage, testutil.JSONResponder(200, protocol.SendMessageResp{Ret: 0}, nil))
	ft.OnContains(protocol.PathGetUploadURL, testutil.JSONResponder(200, protocol.GetUploadURLResp{
		UploadFullURL: "https://cdn-test.example/upload",
		UploadParam:   "up",
	}, nil))
	ft.OnContains("/upload", func(req *http.Request, body []byte) (*http.Response, error) {
		return testutil.BytesResponder(200, nil, map[string]string{"x-encrypted-param": "dl-param-from-cdn"})(req, body)
	})
	ft.OnContains(protocol.PathGetConfig, testutil.JSONResponder(200, protocol.GetConfigResp{Ret: 0, TypingTicket: "tt-1"}, nil))
	ft.OnContains(protocol.PathSendTyping, testutil.JSONResponder(200, map[string]any{"ret": 0}, nil))
	// empty getupdates by default
	ft.OnContains(protocol.PathGetUpdates, testutil.JSONResponder(200, protocol.GetUpdatesResp{Ret: 0}, nil))
}

func TestInboundCursorAndContextTokenThenSendText(t *testing.T) {
	ft := testutil.NewFakeTransport()
	defaultCGI(ft)
	// override getupdates queue
	ft.Enqueue(protocol.PathGetUpdates, testutil.JSONResponder(200, protocol.GetUpdatesResp{
		Ret: 0, GetUpdatesBuf: "cursor-v2",
		Msgs: []protocol.WeixinMessage{{
			FromUserID: "user@im.wechat", ContextToken: "ctx-abc",
			ItemList: []protocol.MessageItem{{Type: protocol.ItemTypeText, TextItem: &protocol.TextItem{Text: "hello bot"}}},
		}},
	}, nil))

	st, sess, accountID := setupAccount(t, ft)
	// re-create with handler
	client := ilink.NewClient(ilink.Config{BaseURL: "https://ilink.test", Token: "test-bot-token", HTTP: ft.Client()})
	ms, _ := media.NewStore(t.TempDir())
	var got session.InboundMessage
	done := make(chan struct{})
	var err error
	sess, err = session.New(withTestCDNFlags(session.Options{
		AccountID: accountID, Client: client, Store: st, HTTP: ft.Client(),
		CDNBaseURL: "https://cdn-test.example/c2c", MediaStore: ms,
		LongPollTimeout: 50 * time.Millisecond, RetryDelay: 10 * time.Millisecond,
		Handler: func(ctx context.Context, msg session.InboundMessage) error {
			got = msg
			if err := sess.SendText(ctx, msg.FromUserID, "pong"); err != nil {
				return err
			}
			close(done)
			return nil
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- sess.Run(ctx) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
	cancel()
	<-errCh

	if got.Text != "hello bot" || got.ContextToken != "ctx-abc" {
		t.Fatalf("%+v", got)
	}
	buf, _ := st.LoadSyncBuf(accountID)
	if buf != "cursor-v2" {
		t.Fatal(buf)
	}
	peer, _ := st.GetPeer(accountID, "user@im.wechat")
	if peer.ContextToken != "ctx-abc" || peer.OutboundCount != 1 {
		t.Fatalf("%+v", peer)
	}
	// send body
	for _, p := range ft.Snapshot() {
		if strings.Contains(p.URL, "sendmessage") {
			var req protocol.SendMessageReq
			_ = json.Unmarshal(p.Body, &req)
			if req.Msg.ContextToken != "ctx-abc" || req.Msg.ItemList[0].TextItem.Text != "pong" {
				t.Fatalf("%+v", req.Msg)
			}
		}
	}
}

func TestPolicyHardBlockNoSend(t *testing.T) {
	ft := testutil.NewFakeTransport()
	defaultCGI(ft)
	st, sess, accountID := setupAccount(t, ft)

	// no token
	if err := sess.SendText(context.Background(), "peer@im.wechat", "hi"); !errors.Is(err, session.ErrSessionWindow) {
		t.Fatalf("%v", err)
	}
	if ft.CountPath("sendmessage") != 0 {
		t.Fatal("send called")
	}

	// touch inbound then age beyond 24h
	now := time.Now()
	_ = st.TouchInbound(accountID, "peer@im.wechat", "ctx", now.Add(-25*time.Hour))
	// rebuild session with fixed clock
	client := ilink.NewClient(ilink.Config{BaseURL: "https://ilink.test", Token: "t", HTTP: ft.Client()})
	ms, _ := media.NewStore(t.TempDir())
	sess, _ = session.New(withTestCDNFlags(session.Options{
		AccountID: accountID, Client: client, Store: st, HTTP: ft.Client(), MediaStore: ms,
		Now: func() time.Time { return now },
	}))
	if err := sess.SendText(context.Background(), "peer@im.wechat", "hi"); !errors.Is(err, session.ErrSessionWindow) {
		t.Fatalf("%v", err)
	}
	if ft.CountPath("sendmessage") != 0 {
		t.Fatal("send on expired window")
	}

	// quota: 10 ok, 11th fails
	_ = st.TouchInbound(accountID, "peer@im.wechat", "ctx", now)
	for i := 0; i < 10; i++ {
		if err := sess.SendText(context.Background(), "peer@im.wechat", "x"); err != nil {
			t.Fatalf("i=%d %v", i, err)
		}
	}
	if err := sess.SendText(context.Background(), "peer@im.wechat", "x"); !errors.Is(err, session.ErrOutboundQuota) {
		t.Fatalf("%v", err)
	}
	sends := ft.CountPath("sendmessage")
	if sends != 10 {
		t.Fatalf("sends=%d", sends)
	}
	// re-inbound resets
	_ = st.TouchInbound(accountID, "peer@im.wechat", "ctx2", now)
	if err := sess.SendText(context.Background(), "peer@im.wechat", "ok"); err != nil {
		t.Fatal(err)
	}
}

func TestMarkdownAndChunkAndSkip(t *testing.T) {
	ft := testutil.NewFakeTransport()
	defaultCGI(ft)
	st, sess, accountID := setupAccount(t, ft)
	now := time.Now()
	_ = st.TouchInbound(accountID, "peer@im.wechat", "ctx", now)

	// markdown strips image
	if err := sess.SendText(context.Background(), "peer@im.wechat", "hi ![x](u)"); err != nil {
		t.Fatal(err)
	}
	// skip keeps **
	if err := sess.SendTextOpts(context.Background(), "peer@im.wechat", "**x**", session.SendTextOptions{SkipMarkdownFilter: true}); err != nil {
		t.Fatal(err)
	}
	// 4500 runes → 2 sends (plus previous 2)
	long := strings.Repeat("你", 4500)
	if err := sess.SendText(context.Background(), "peer@im.wechat", long); err != nil {
		t.Fatal(err)
	}
	// collect text payloads
	var texts []string
	for _, p := range ft.Snapshot() {
		if !strings.Contains(p.URL, "sendmessage") {
			continue
		}
		var req protocol.SendMessageReq
		_ = json.Unmarshal(p.Body, &req)
		if req.Msg != nil && len(req.Msg.ItemList) > 0 && req.Msg.ItemList[0].TextItem != nil {
			texts = append(texts, req.Msg.ItemList[0].TextItem.Text)
		}
	}
	if texts[0] != "hi " {
		t.Fatalf("filtered %q", texts[0])
	}
	if texts[1] != "**x**" {
		t.Fatalf("skip %q", texts[1])
	}
	if len(texts) < 4 {
		t.Fatalf("chunk count %d texts=%v", len(texts), texts)
	}
	joined := texts[2] + texts[3]
	if joined != long {
		t.Fatalf("chunk concat len %d", len([]rune(joined)))
	}
}

func TestQuotaBlocksBeforeChunkSend(t *testing.T) {
	ft := testutil.NewFakeTransport()
	defaultCGI(ft)
	st, sess, accountID := setupAccount(t, ft)
	now := time.Now()
	_ = st.TouchInbound(accountID, "peer@im.wechat", "ctx", now)
	// fill quota
	for i := 0; i < 10; i++ {
		_ = sess.SendText(context.Background(), "peer@im.wechat", "x")
	}
	before := ft.CountPath("sendmessage")
	long := strings.Repeat("a", 4500)
	if err := sess.SendText(context.Background(), "peer@im.wechat", long); !errors.Is(err, session.ErrOutboundQuota) {
		t.Fatalf("%v", err)
	}
	if ft.CountPath("sendmessage") != before {
		t.Fatal("chunk must not send when quota exceeded")
	}
}

func TestSendImagePipeline(t *testing.T) {
	ft := testutil.NewFakeTransport()
	defaultCGI(ft)
	st, sess, accountID := setupAccount(t, ft)
	_ = st.TouchInbound(accountID, "peer@im.wechat", "ctx-img", time.Now())
	imgPath := filepath.Join(t.TempDir(), "pic.bin")
	plain := []byte("fake-image-bytes-0123456789")
	_ = os.WriteFile(imgPath, plain, 0o600)
	if err := sess.SendImageFile(context.Background(), "peer@im.wechat", imgPath, ""); err != nil {
		t.Fatal(err)
	}
	var aesHex string
	var sawUpload, sawCDN, sawSend bool
	for _, p := range ft.Snapshot() {
		if strings.Contains(p.URL, "getuploadurl") {
			sawUpload = true
			var req protocol.GetUploadURLReq
			_ = json.Unmarshal(p.Body, &req)
			if req.MediaType != protocol.UploadMediaImage || !req.NoNeedThumb {
				t.Fatalf("%+v", req)
			}
			aesHex = req.AESKey
		}
		if p.Header.Get("Content-Type") == "application/octet-stream" {
			sawCDN = true
			key, _ := hex.DecodeString(aesHex)
			out, err := media.DecryptAES128ECB(p.Body, key)
			if err != nil || !bytes.Equal(out, plain) {
				t.Fatalf("cipher %v", err)
			}
		}
		if strings.Contains(p.URL, "sendmessage") {
			sawSend = true
			var req protocol.SendMessageReq
			_ = json.Unmarshal(p.Body, &req)
			img := req.Msg.ItemList[0].ImageItem
			if img.Media.EncryptType != 1 || img.Media.EncryptQueryParam != "dl-param-from-cdn" {
				t.Fatalf("%+v", img)
			}
		}
	}
	if !sawUpload || !sawCDN || !sawSend {
		t.Fatal("pipeline incomplete")
	}
}

func TestSendFileAttachmentPipeline(t *testing.T) {
	ft := testutil.NewFakeTransport()
	defaultCGI(ft)
	st, sess, accountID := setupAccount(t, ft)
	_ = st.TouchInbound(accountID, "peer@im.wechat", "ctx-file", time.Now())
	path := filepath.Join(t.TempDir(), "doc.pdf")
	plain := []byte("%PDF-fake-content-123456")
	_ = os.WriteFile(path, plain, 0o600)
	if err := sess.SendFileAttachment(context.Background(), "peer@im.wechat", path); err != nil {
		t.Fatal(err)
	}
	var aesHex string
	var sawUpload, sawCDN, sawSend bool
	for _, p := range ft.Snapshot() {
		if strings.Contains(p.URL, "getuploadurl") {
			sawUpload = true
			var req protocol.GetUploadURLReq
			_ = json.Unmarshal(p.Body, &req)
			if req.MediaType != protocol.UploadMediaFile {
				t.Fatalf("media_type %d want FILE", req.MediaType)
			}
			if !req.NoNeedThumb {
				t.Fatal("no_need_thumb")
			}
			aesHex = req.AESKey
		}
		if p.Header.Get("Content-Type") == "application/octet-stream" {
			sawCDN = true
			key, _ := hex.DecodeString(aesHex)
			out, err := media.DecryptAES128ECB(p.Body, key)
			if err != nil || !bytes.Equal(out, plain) {
				t.Fatalf("cipher %v", err)
			}
		}
		if strings.Contains(p.URL, "sendmessage") {
			sawSend = true
			var req protocol.SendMessageReq
			_ = json.Unmarshal(p.Body, &req)
			it := req.Msg.ItemList[0]
			if it.Type != protocol.ItemTypeFile || it.FileItem == nil || it.FileItem.Media.EncryptType != 1 {
				t.Fatalf("%+v", it)
			}
			if it.FileItem.FileName != "doc.pdf" {
				t.Fatalf("name %q", it.FileItem.FileName)
			}
		}
	}
	if !sawUpload || !sawCDN || !sawSend {
		t.Fatal("file pipeline incomplete")
	}
}

func TestSendVideoFileAndVoiceAndTools(t *testing.T) {
	ft := testutil.NewFakeTransport()
	defaultCGI(ft)
	st, sess, accountID := setupAccount(t, ft)
	_ = st.TouchInbound(accountID, "peer@im.wechat", "ctx", time.Now())

	vpath := filepath.Join(t.TempDir(), "v.mp4")
	_ = os.WriteFile(vpath, []byte("video-bytes-here!!!!!"), 0o600)
	if err := sess.SendVideoFile(context.Background(), "peer@im.wechat", vpath, "cap"); err != nil {
		t.Fatal(err)
	}

	// voice silk passthrough
	spath := filepath.Join(t.TempDir(), "a.silk")
	_ = os.WriteFile(spath, append([]byte("#!SILK_V3"), 1, 2, 3, 4), 0o600)
	if err := sess.SendVoice(context.Background(), "peer@im.wechat", spath); err != nil {
		t.Fatal(err)
	}
	// WAV without encoder → clear error (force NilSilkCodec)
	client := ilink.NewClient(ilink.Config{BaseURL: "https://ilink.test", Token: "test-bot-token", HTTP: ft.Client()})
	ms, _ := media.NewStore(t.TempDir())
	sessNil, _ := session.New(withTestCDNFlags(session.Options{
		AccountID: accountID, Client: client, Store: st, HTTP: ft.Client(), MediaStore: ms,
		SilkCodec: media.NilSilkCodec{},
	}))
	wpath := filepath.Join(t.TempDir(), "a.wav")
	_ = os.WriteFile(wpath, media.PCMToWAV([]byte{0, 0}, 24000), 0o600)
	if err := sessNil.SendVoice(context.Background(), "peer@im.wechat", wpath); err == nil {
		t.Fatal("expected encode error")
	}

	if err := sess.SendToolStart(context.Background(), "peer@im.wechat", "search", "c1"); err != nil {
		t.Fatal(err)
	}
	if err := sess.SendToolResult(context.Background(), "peer@im.wechat", "search", "c1", "completed"); err != nil {
		t.Fatal(err)
	}

	var sawVoice, sawToolStart, sawToolResult, sawVideo bool
	for _, p := range ft.Snapshot() {
		if !strings.Contains(p.URL, "sendmessage") {
			continue
		}
		var req protocol.SendMessageReq
		_ = json.Unmarshal(p.Body, &req)
		if req.Msg == nil || len(req.Msg.ItemList) == 0 {
			continue
		}
		it := req.Msg.ItemList[0]
		switch it.Type {
		case protocol.ItemTypeVoice:
			sawVoice = true
			if it.VoiceItem.Media.EncryptType != 1 {
				t.Fatal("voice encrypt_type")
			}
		case protocol.ItemTypeVideo:
			sawVideo = true
		case protocol.ItemTypeToolCallStart:
			sawToolStart = true
		case protocol.ItemTypeToolCallResult:
			sawToolResult = true
		}
	}
	if !sawVoice || !sawVideo || !sawToolStart || !sawToolResult {
		t.Fatalf("voice=%v video=%v start=%v result=%v", sawVoice, sawVideo, sawToolStart, sawToolResult)
	}
}

func TestSendMediaVoiceEncodesLikeSendVoice(t *testing.T) {
	ft := testutil.NewFakeTransport()
	defaultCGI(ft)
	st, sess, accountID := setupAccount(t, ft)
	_ = st.TouchInbound(accountID, "peer@im.wechat", "ctx", time.Now())

	// WAV input via SendMedia must go through SILK encode before getuploadurl VOICE
	pcm := make([]byte, media.SILKSampleRate/5*2)
	wpath := filepath.Join(t.TempDir(), "v.wav")
	_ = os.WriteFile(wpath, media.PCMToWAV(pcm, media.SILKSampleRate), 0o600)
	if err := sess.SendMedia(context.Background(), "peer@im.wechat", wpath, ""); err != nil {
		t.Fatal(err)
	}
	var sawVoiceUpload, sawVoiceItem bool
	for _, p := range ft.Snapshot() {
		if strings.Contains(p.URL, "getuploadurl") {
			var req protocol.GetUploadURLReq
			_ = json.Unmarshal(p.Body, &req)
			if req.MediaType == protocol.UploadMediaVoice {
				sawVoiceUpload = true
			}
		}
		if strings.Contains(p.URL, "sendmessage") {
			var req protocol.SendMessageReq
			_ = json.Unmarshal(p.Body, &req)
			if req.Msg != nil && len(req.Msg.ItemList) > 0 && req.Msg.ItemList[0].Type == protocol.ItemTypeVoice {
				sawVoiceItem = true
				if req.Msg.ItemList[0].VoiceItem.Media.EncryptType != 1 {
					t.Fatal("encrypt_type")
				}
			}
		}
	}
	if !sawVoiceUpload || !sawVoiceItem {
		t.Fatalf("voice upload=%v item=%v", sawVoiceUpload, sawVoiceItem)
	}
}

func TestSendMediaCaptionReservesOnce(t *testing.T) {
	ft := testutil.NewFakeTransport()
	defaultCGI(ft)
	st, sess, accountID := setupAccount(t, ft)
	now := time.Now()
	_ = st.TouchInbound(accountID, "peer@im.wechat", "ctx", now)
	// use 9 of 10 slots; caption+media needs 2 → must fail with zero new sends
	for i := 0; i < 9; i++ {
		if err := sess.SendText(context.Background(), "peer@im.wechat", "x"); err != nil {
			t.Fatal(err)
		}
	}
	before := ft.CountPath("sendmessage")
	img := filepath.Join(t.TempDir(), "x.png")
	_ = os.WriteFile(img, []byte("png"), 0o600)
	err := sess.SendMedia(context.Background(), "peer@im.wechat", img, "caption")
	if !errors.Is(err, session.ErrOutboundQuota) {
		t.Fatalf("%v", err)
	}
	if ft.CountPath("sendmessage") != before {
		t.Fatal("must not partial-send caption when caption+media exceeds quota")
	}
	if ft.CountPath("getuploadurl") != 0 {
		t.Fatal("must not start upload when reserve fails")
	}
}

func TestSendMediaAndURL(t *testing.T) {
	ft := testutil.NewFakeTransport()
	defaultCGI(ft)
	// remote url download
	ft.OnContains("/remote", testutil.BytesResponder(200, []byte("png-data"), map[string]string{"Content-Type": "image/png"}))
	st, sess, accountID := setupAccount(t, ft)
	_ = st.TouchInbound(accountID, "peer@im.wechat", "ctx", time.Now())

	img := filepath.Join(t.TempDir(), "x.png")
	_ = os.WriteFile(img, []byte("png"), 0o600)
	if err := sess.SendMedia(context.Background(), "peer@im.wechat", img, ""); err != nil {
		t.Fatal(err)
	}
	if err := sess.SendMediaURL(context.Background(), "peer@im.wechat", "https://cdn-test.example/remote/a.png", ""); err != nil {
		t.Fatal(err)
	}
	// 404
	ft2 := testutil.NewFakeTransport()
	defaultCGI(ft2)
	ft2.OnContains("/missing", testutil.JSONResponder(404, map[string]string{"e": "no"}, nil))
	st2, sess2, id2 := setupAccount(t, ft2)
	_ = st2.TouchInbound(id2, "peer@im.wechat", "ctx", time.Now())
	if err := sess2.SendMediaURL(context.Background(), "peer@im.wechat", "https://cdn-test.example/missing", ""); err == nil {
		t.Fatal("expected 404")
	}
}

func TestSendItemAndUserMessageFromError(t *testing.T) {
	ft := testutil.NewFakeTransport()
	defaultCGI(ft)
	st, sess, accountID := setupAccount(t, ft)
	_ = st.TouchInbound(accountID, "u", "c", time.Now())
	if err := sess.SendItem(context.Background(), "u", protocol.MessageItem{
		Type: 99, TextItem: &protocol.TextItem{Text: "custom"},
	}); err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, p := range ft.Snapshot() {
		if strings.Contains(p.URL, "sendmessage") {
			var req protocol.SendMessageReq
			_ = json.Unmarshal(p.Body, &req)
			if req.Msg.ItemList[0].Type == 99 {
				saw = true
			}
		}
	}
	if !saw {
		t.Fatal("custom type")
	}
	if !strings.Contains(session.UserMessageFromError(session.ErrOutboundQuota), "上限") {
		t.Fatal("quota msg")
	}
	if !strings.Contains(session.UserMessageFromError(session.ErrSessionWindow), "会话") {
		t.Fatal("window msg")
	}
	if !strings.Contains(session.UserMessageFromError(&ilink.APIError{Ret: -2}), "频繁") {
		t.Fatal("rate msg")
	}
}

func TestRateLimitRetry(t *testing.T) {
	ft := testutil.NewFakeTransport()
	defaultCGI(ft)
	// first two sendmessage rate limit, then ok — use queue
	ft.Enqueue(protocol.PathSendMessage,
		testutil.JSONResponder(200, protocol.SendMessageResp{Ret: -2, ErrMsg: "rate limited"}, nil),
		testutil.JSONResponder(200, protocol.SendMessageResp{Ret: -2, ErrMsg: "rate limited"}, nil),
		testutil.JSONResponder(200, protocol.SendMessageResp{Ret: 0}, nil),
	)
	dir := t.TempDir()
	st, _ := state.NewStore(dir)
	_ = st.RegisterAccountID("b")
	_ = st.SaveAccount("b", state.Account{Token: "t", BaseURL: "https://ilink.test"})
	_ = st.TouchInbound("b", "u", "c", time.Now())
	ms, _ := media.NewStore(t.TempDir())
	client := ilink.NewClient(ilink.Config{BaseURL: "https://ilink.test", Token: "t", HTTP: ft.Client()})
	sess, err := session.New(withTestCDNFlags(session.Options{
		AccountID: "b", Client: client, Store: st, HTTP: ft.Client(), MediaStore: ms,
		RetryRateLimit: 3, RateLimitBackoff: time.Millisecond,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.SendText(context.Background(), "u", "hi"); err != nil {
		t.Fatal(err)
	}
	// quota error does not retry: fill and check single attempt path via Count
	// exhausted retries
	ft2 := testutil.NewFakeTransport()
	defaultCGI(ft2)
	for i := 0; i < 5; i++ {
		ft2.Enqueue(protocol.PathSendMessage, testutil.JSONResponder(200, protocol.SendMessageResp{Ret: -2, ErrMsg: "rate limited"}, nil))
	}
	client2 := ilink.NewClient(ilink.Config{BaseURL: "https://ilink.test", Token: "t", HTTP: ft2.Client()})
	_ = st.TouchInbound("b", "u2", "c", time.Now())
	sess2, _ := session.New(withTestCDNFlags(session.Options{
		AccountID: "b", Client: client2, Store: st, HTTP: ft2.Client(), MediaStore: ms,
		RetryRateLimit: 2, RateLimitBackoff: time.Millisecond,
	}))
	err = sess2.SendText(context.Background(), "u2", "hi")
	if !ilink.IsRateLimited(err) {
		t.Fatalf("%v", err)
	}
}

func TestInboundImageDownloadAndMediaError(t *testing.T) {
	key := []byte("0123456789abcdef")
	plain := []byte("img-plain-text-data!!")
	ct, _ := media.EncryptAES128ECB(plain, key)
	hexKey := hex.EncodeToString(key)
	b64 := base64.StdEncoding.EncodeToString(key)

	ft := testutil.NewFakeTransport()
	defaultCGI(ft)
	ft.Enqueue(protocol.PathGetUpdates, testutil.JSONResponder(200, protocol.GetUpdatesResp{
		Ret: 0, GetUpdatesBuf: "c1",
		Msgs: []protocol.WeixinMessage{{
			FromUserID: "u", ContextToken: "ctx",
			ItemList: []protocol.MessageItem{{
				Type: protocol.ItemTypeImage,
				ImageItem: &protocol.ImageItem{
					AESKey: hexKey,
					Media:  &protocol.CDNMedia{FullURL: "https://cdn-test.example/download", AESKey: b64},
				},
			}, {
				Type: protocol.ItemTypeText, TextItem: &protocol.TextItem{Text: "see pic"},
			}},
		}},
	}, nil))
	ft.OnContains("/download", testutil.BytesResponder(200, ct, nil))

	st, _, accountID := setupAccount(t, ft)
	ms, _ := media.NewStore(t.TempDir())
	client := ilink.NewClient(ilink.Config{BaseURL: "https://ilink.test", Token: "test-bot-token", HTTP: ft.Client()})
	var got session.InboundMessage
	done := make(chan struct{})
	var sess *session.Session
	sess, _ = session.New(withTestCDNFlags(session.Options{
		AccountID: accountID, Client: client, Store: st, HTTP: ft.Client(), MediaStore: ms,
		CDNBaseURL: "https://cdn-test.example/c2c",
		LongPollTimeout: 50 * time.Millisecond, RetryDelay: 10 * time.Millisecond,
		Handler: func(ctx context.Context, msg session.InboundMessage) error {
			got = msg
			close(done)
			return nil
		},
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sess.Run(ctx)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
	cancel()
	if got.Text != "see pic" || len(got.Media) != 1 || got.Media[0].Kind != "image" {
		t.Fatalf("%+v", got)
	}
	data, _ := os.ReadFile(got.Media[0].Path)
	if !bytes.Equal(data, plain) {
		t.Fatal("media path content")
	}
}

func TestParseInboundQuotes(t *testing.T) {
	tests := []struct {
		name      string
		raw       protocol.WeixinMessage
		want      string
		quoteKind string
	}{
		{"title only", protocol.WeixinMessage{ItemList: []protocol.MessageItem{{
			Type: 1, TextItem: &protocol.TextItem{Text: "reply"},
			RefMsg: &protocol.RefMessage{Title: "t"},
		}}}, "「t」\nreply", ""},
		{"title+text", protocol.WeixinMessage{ItemList: []protocol.MessageItem{{
			Type: 1, TextItem: &protocol.TextItem{Text: "r"},
			RefMsg: &protocol.RefMessage{Title: "t", MessageItem: &protocol.MessageItem{
				Type: 1, TextItem: &protocol.TextItem{Text: "q"},
			}},
		}}}, "「t: q」\nr", ""},
		{"empty ref", protocol.WeixinMessage{ItemList: []protocol.MessageItem{{
			Type: 1, TextItem: &protocol.TextItem{Text: "only"},
		}}}, "only", ""},
		{"quote image", protocol.WeixinMessage{ItemList: []protocol.MessageItem{{
			Type: 1, TextItem: &protocol.TextItem{Text: "see"},
			RefMsg: &protocol.RefMessage{Title: "图", MessageItem: &protocol.MessageItem{
				Type: protocol.ItemTypeImage, ImageItem: &protocol.ImageItem{},
			}},
		}}}, "「图」\nsee", "image"},
		{"voice asr", protocol.WeixinMessage{ItemList: []protocol.MessageItem{{
			Type: protocol.ItemTypeVoice,
			VoiceItem: &protocol.VoiceItem{Text: "said hello"},
		}}}, "said hello", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := session.ParseInbound(tt.raw)
			if p.Text != tt.want {
				t.Fatalf("got %q want %q", p.Text, tt.want)
			}
			if tt.quoteKind != "" {
				if p.Quote == nil || p.Quote.Media == nil || p.Quote.Media.Kind != tt.quoteKind {
					t.Fatalf("quote media %+v", p.Quote)
				}
			}
		})
	}
}

func TestInboundVoiceASRAndPath(t *testing.T) {
	key := []byte("0123456789abcdef")
	pcm := make([]byte, media.SILKSampleRate/5*2)
	// DefaultSilkCodec: RealSilkCodec (!race) or FramedPCMSilkCodec (race)
	silk, err := media.DefaultSilkCodec.EncodePCMToSilk(pcm, media.SILKSampleRate)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := media.EncryptAES128ECB(silk, key)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString(key)

	ft := testutil.NewFakeTransport()
	defaultCGI(ft)
	ft.Enqueue(protocol.PathGetUpdates, testutil.JSONResponder(200, protocol.GetUpdatesResp{
		Ret: 0, GetUpdatesBuf: "c1",
		Msgs: []protocol.WeixinMessage{{
			FromUserID: "u", ContextToken: "ctx",
			ItemList: []protocol.MessageItem{{
				Type: protocol.ItemTypeVoice,
				VoiceItem: &protocol.VoiceItem{
					Text:  "asr hello",
					Media: &protocol.CDNMedia{FullURL: "https://cdn-test.example/download", AESKey: b64},
				},
			}},
		}},
	}, nil))
	ft.OnContains("/download", testutil.BytesResponder(200, ct, nil))

	st, _, accountID := setupAccount(t, ft)
	ms, _ := media.NewStore(t.TempDir())
	client := ilink.NewClient(ilink.Config{BaseURL: "https://ilink.test", Token: "test-bot-token", HTTP: ft.Client()})
	var got session.InboundMessage
	done := make(chan struct{})
	sess, _ := session.New(withTestCDNFlags(session.Options{
		AccountID: accountID, Client: client, Store: st, HTTP: ft.Client(), MediaStore: ms,
		CDNBaseURL: "https://cdn-test.example/c2c",
		LongPollTimeout: 50 * time.Millisecond, RetryDelay: 10 * time.Millisecond,
		Handler: func(ctx context.Context, msg session.InboundMessage) error {
			got = msg
			close(done)
			return nil
		},
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sess.Run(ctx)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
	cancel()
	if got.Text != "asr hello" {
		t.Fatalf("text %q", got.Text)
	}
	if len(got.Media) != 1 || got.Media[0].Kind != "voice" {
		t.Fatalf("media %+v", got.Media)
	}
	if got.Media[0].MIME != "audio/wav" {
		t.Fatalf("mime %s want audio/wav", got.Media[0].MIME)
	}
}

func TestTypingWithTyping(t *testing.T) {
	ft := testutil.NewFakeTransport()
	defaultCGI(ft)
	st, sess, accountID := setupAccount(t, ft)
	_ = st.TouchInbound(accountID, "u", "c", time.Now())
	err := sess.WithTyping(context.Background(), "u", func(ctx context.Context) error {
		time.Sleep(50 * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var status1, status2 bool
	for _, p := range ft.Snapshot() {
		if !strings.Contains(p.URL, "sendtyping") {
			continue
		}
		var req protocol.SendTypingReq
		_ = json.Unmarshal(p.Body, &req)
		if req.Status == 1 {
			status1 = true
		}
		if req.Status == 2 {
			status2 = true
		}
	}
	if !status1 || !status2 {
		t.Fatalf("typing status1=%v status2=%v", status1, status2)
	}
}

func TestConfigCacheHit(t *testing.T) {
	ft := testutil.NewFakeTransport()
	defaultCGI(ft)
	st, sess, accountID := setupAccount(t, ft)
	_ = st.TouchInbound(accountID, "u", "c", time.Now())
	_ = sess.StartTyping(context.Background(), "u")
	_ = sess.StartTyping(context.Background(), "u")
	if n := ft.CountPath("getconfig"); n != 1 {
		t.Fatalf("getconfig count %d", n)
	}
}

func TestConfigCacheBackoffAndTypingNoop(t *testing.T) {
	ft := testutil.NewFakeTransport()
	defaultCGI(ft)
	// fail getconfig
	ft.OnContains(protocol.PathGetConfig, testutil.JSONResponder(200, protocol.GetConfigResp{Ret: 1, ErrMsg: "fail"}, nil))
	// clear sticky success - OnContains is sticky and last registered wins? Looking at RoundTrip - queues first then contains in order. First match on contains for PathGetConfig was success from defaultCGI. Need to not use defaultCGI getconfig.

	ft2 := testutil.NewFakeTransport()
	ft2.OnContains(protocol.PathNotifyStart, testutil.JSONResponder(200, protocol.NotifyResp{Ret: 0}, nil))
	ft2.OnContains(protocol.PathNotifyStop, testutil.JSONResponder(200, protocol.NotifyResp{Ret: 0}, nil))
	ft2.OnContains(protocol.PathGetConfig, testutil.JSONResponder(200, protocol.GetConfigResp{Ret: 1, ErrMsg: "fail"}, nil))
	ft2.OnContains(protocol.PathSendTyping, testutil.JSONResponder(200, map[string]any{"ret": 0}, nil))

	st, err := state.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = st.RegisterAccountID("b")
	_ = st.SaveAccount("b", state.Account{Token: "t", BaseURL: "https://ilink.test"})
	_ = st.TouchInbound("b", "u", "c", time.Now())
	ms, _ := media.NewStore(t.TempDir())
	client := ilink.NewClient(ilink.Config{BaseURL: "https://ilink.test", Token: "t", HTTP: ft2.Client()})
	sess, _ := session.New(withTestCDNFlags(session.Options{
		AccountID: "b", Client: client, Store: st, HTTP: ft2.Client(), MediaStore: ms,
	}))
	var ran bool
	err = sess.WithTyping(context.Background(), "u", func(ctx context.Context) error {
		ran = true
		return nil
	})
	if err != nil || !ran {
		t.Fatalf("fn err=%v ran=%v", err, ran)
	}
	if ft2.CountPath("sendtyping") != 0 {
		t.Fatal("typing should be no-op without ticket")
	}
	// second call still no typing
	_ = sess.StartTyping(context.Background(), "u")
	if ft2.CountPath("sendtyping") != 0 {
		t.Fatal("still no-op")
	}
}

func TestSendMediaURLMaxBytes(t *testing.T) {
	ft := testutil.NewFakeTransport()
	defaultCGI(ft)
	ft.OnContains("/remote", testutil.BytesResponder(200, bytes.Repeat([]byte("x"), 100), map[string]string{"Content-Type": "image/png"}))
	st, sess, accountID := setupAccount(t, ft)
	_ = st.TouchInbound(accountID, "peer@im.wechat", "ctx", time.Now())
	// rebuild with tiny limit
	client := ilink.NewClient(ilink.Config{BaseURL: "https://ilink.test", Token: "test-bot-token", HTTP: ft.Client()})
	ms, _ := media.NewStore(t.TempDir())
	sess, _ = session.New(withTestCDNFlags(session.Options{
		AccountID: accountID, Client: client, Store: st, HTTP: ft.Client(), MediaStore: ms,
		MaxMediaURLBytes: 10,
	}))
	err := sess.SendMediaURL(context.Background(), "peer@im.wechat", "https://cdn-test.example/remote/a.png", "")
	if err == nil || !strings.Contains(err.Error(), "MaxMediaURLBytes") {
		t.Fatalf("%v", err)
	}
	if ft.CountPath("getuploadurl") != 0 {
		t.Fatal("must not upload when oversize")
	}
}

func TestChunkReserveAllOrNothing(t *testing.T) {
	ft := testutil.NewFakeTransport()
	defaultCGI(ft)
	st, sess, accountID := setupAccount(t, ft)
	now := time.Now()
	_ = st.TouchInbound(accountID, "peer@im.wechat", "ctx", now)
	// 9 used, 1 remaining — 2-chunk body should not send either
	for i := 0; i < 9; i++ {
		if err := sess.SendText(context.Background(), "peer@im.wechat", "x"); err != nil {
			t.Fatal(err)
		}
	}
	before := ft.CountPath("sendmessage")
	long := strings.Repeat("你", 4500) // 2 chunks
	if err := sess.SendText(context.Background(), "peer@im.wechat", long); !errors.Is(err, session.ErrOutboundQuota) {
		t.Fatalf("%v", err)
	}
	if ft.CountPath("sendmessage") != before {
		t.Fatal("partial chunk send not allowed")
	}
}

func TestConcurrentSendRespectsQuota(t *testing.T) {
	ft := testutil.NewFakeTransport()
	defaultCGI(ft)
	st, sess, accountID := setupAccount(t, ft)
	_ = st.TouchInbound(accountID, "peer@im.wechat", "ctx", time.Now())
	var wg sync.WaitGroup
	var okN, errN atomic.Int32
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := sess.SendText(context.Background(), "peer@im.wechat", "x"); err != nil {
				errN.Add(1)
				return
			}
			okN.Add(1)
		}()
	}
	wg.Wait()
	if okN.Load() != 10 {
		t.Fatalf("ok=%d err=%d sends=%d", okN.Load(), errN.Load(), ft.CountPath("sendmessage"))
	}
	if ft.CountPath("sendmessage") != 10 {
		t.Fatalf("sends %d", ft.CountPath("sendmessage"))
	}
}

func TestRunNotifyAndStalePauseAndHandlerError(t *testing.T) {
	ft := testutil.NewFakeTransport()
	defaultCGI(ft)
	// stale then ok messages
	ft.Enqueue(protocol.PathGetUpdates,
		testutil.JSONResponder(200, protocol.GetUpdatesResp{Ret: protocol.StaleTokenErrCode, ErrCode: protocol.StaleTokenErrCode}, nil),
		testutil.JSONResponder(200, protocol.GetUpdatesResp{
			Ret: 0, GetUpdatesBuf: "c1",
			Msgs: []protocol.WeixinMessage{{
				FromUserID: "u", ContextToken: "ctx",
				ItemList: []protocol.MessageItem{{Type: 1, TextItem: &protocol.TextItem{Text: "a"}}},
			}},
		}, nil),
		testutil.JSONResponder(200, protocol.GetUpdatesResp{
			Ret: 0, GetUpdatesBuf: "c2",
			Msgs: []protocol.WeixinMessage{{
				FromUserID: "u", ContextToken: "ctx",
				ItemList: []protocol.MessageItem{{Type: 1, TextItem: &protocol.TextItem{Text: "b"}}},
			}},
		}, nil),
	)
	st, _, accountID := setupAccount(t, ft)
	ms, _ := media.NewStore(t.TempDir())
	client := ilink.NewClient(ilink.Config{BaseURL: "https://ilink.test", Token: "test-bot-token", HTTP: ft.Client()})
	var handlerErrs atomic.Int32
	var texts []string
	var mu sync.Mutex
	done := make(chan struct{})
	sess, _ := session.New(withTestCDNFlags(session.Options{
		AccountID: accountID, Client: client, Store: st, HTTP: ft.Client(), MediaStore: ms,
		LongPollTimeout: 30 * time.Millisecond, RetryDelay: 5 * time.Millisecond,
		StaleTokenPause: 20 * time.Millisecond,
		OnHandlerError: func(msg session.InboundMessage, err error) {
			handlerErrs.Add(1)
		},
		Handler: func(ctx context.Context, msg session.InboundMessage) error {
			mu.Lock()
			texts = append(texts, msg.Text)
			n := len(texts)
			mu.Unlock()
			if msg.Text == "a" {
				return errors.New("boom")
			}
			if n >= 2 {
				close(done)
			}
			return nil
		},
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- sess.Run(ctx) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
	cancel()
	<-errCh
	if handlerErrs.Load() != 1 {
		t.Fatalf("handler errs %d", handlerErrs.Load())
	}
	if ft.CountPath("notifystart") < 1 || ft.CountPath("notifystop") < 1 {
		t.Fatal("notify")
	}
	buf, _ := st.LoadSyncBuf(accountID)
	if buf != "c2" {
		t.Fatalf("cursor %q", buf)
	}
}

func TestManagerTwoAccounts(t *testing.T) {
	ftA := testutil.NewFakeTransport()
	defaultCGI(ftA)
	ftA.Enqueue(protocol.PathGetUpdates, testutil.JSONResponder(200, protocol.GetUpdatesResp{
		Ret: 0, GetUpdatesBuf: "A1",
		Msgs: []protocol.WeixinMessage{{FromUserID: "u", ContextToken: "c", ItemList: []protocol.MessageItem{{Type: 1, TextItem: &protocol.TextItem{Text: "for-a"}}}}},
	}, nil))
	ftB := testutil.NewFakeTransport()
	defaultCGI(ftB)
	ftB.Enqueue(protocol.PathGetUpdates, testutil.JSONResponder(200, protocol.GetUpdatesResp{
		Ret: 0, GetUpdatesBuf: "B1",
		Msgs: []protocol.WeixinMessage{{FromUserID: "u", ContextToken: "c", ItemList: []protocol.MessageItem{{Type: 1, TextItem: &protocol.TextItem{Text: "for-b"}}}}},
	}, nil))

	dir := t.TempDir()
	st, _ := state.NewStore(dir)
	_ = st.RegisterAccountID("acc-a")
	_ = st.SaveAccount("acc-a", state.Account{Token: "ta", BaseURL: "https://ilink.test"})
	_ = st.RegisterAccountID("acc-b")
	_ = st.SaveAccount("acc-b", state.Account{Token: "tb", BaseURL: "https://ilink.test"})
	msA, _ := media.NewStore(t.TempDir())
	msB, _ := media.NewStore(t.TempDir())

	var gotA, gotB atomic.Bool
	clientA := ilink.NewClient(ilink.Config{BaseURL: "https://ilink.test", Token: "ta", HTTP: ftA.Client()})
	sessA, _ := session.New(withTestCDNFlags(session.Options{
		AccountID: "acc-a", Client: clientA, Store: st, HTTP: ftA.Client(), MediaStore: msA,
		LongPollTimeout: 40 * time.Millisecond, RetryDelay: 10 * time.Millisecond,
		Handler: func(ctx context.Context, msg session.InboundMessage) error {
			if msg.Text == "for-a" {
				gotA.Store(true)
			}
			return nil
		},
	}))
	clientB := ilink.NewClient(ilink.Config{BaseURL: "https://ilink.test", Token: "tb", HTTP: ftB.Client()})
	sessB, _ := session.New(withTestCDNFlags(session.Options{
		AccountID: "acc-b", Client: clientB, Store: st, HTTP: ftB.Client(), MediaStore: msB,
		LongPollTimeout: 40 * time.Millisecond, RetryDelay: 10 * time.Millisecond,
		Handler: func(ctx context.Context, msg session.InboundMessage) error {
			if msg.Text == "for-b" {
				gotB.Store(true)
			}
			return nil
		},
	}))

	mgr := session.NewManager()
	ctx := context.Background()
	if err := mgr.Start(ctx, sessA); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Start(ctx, sessB); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !(gotA.Load() && gotB.Load()) {
		time.Sleep(20 * time.Millisecond)
	}
	if !gotA.Load() || !gotB.Load() {
		t.Fatalf("gotA=%v gotB=%v", gotA.Load(), gotB.Load())
	}
	bufA, _ := st.LoadSyncBuf("acc-a")
	bufB, _ := st.LoadSyncBuf("acc-b")
	if bufA != "A1" || bufB != "B1" {
		t.Fatalf("cursors A=%q B=%q", bufA, bufB)
	}
	_ = mgr.Stop("acc-a")
	if _, ok := mgr.Get("acc-a"); ok {
		t.Fatal("a should be gone")
	}
	if _, ok := mgr.Get("acc-b"); !ok {
		t.Fatal("b should remain")
	}
	// B still running: empty polls continue without error
	time.Sleep(50 * time.Millisecond)
	mgr.StopAll()
}

func TestDownloadFailureStillDeliversText(t *testing.T) {
	ft := testutil.NewFakeTransport()
	defaultCGI(ft)
	ft.Enqueue(protocol.PathGetUpdates, testutil.JSONResponder(200, protocol.GetUpdatesResp{
		Ret: 0, GetUpdatesBuf: "c",
		Msgs: []protocol.WeixinMessage{{
			FromUserID: "u", ContextToken: "ctx",
			ItemList: []protocol.MessageItem{
				{Type: protocol.ItemTypeImage, ImageItem: &protocol.ImageItem{
					AESKey: hex.EncodeToString([]byte("0123456789abcdef")),
					Media:  &protocol.CDNMedia{FullURL: "https://cdn-test.example/download"},
				}},
				{Type: protocol.ItemTypeText, TextItem: &protocol.TextItem{Text: "hello"}},
			},
		}},
	}, nil))
	// CDN download fails
	ft.OnContains("/download", func(req *http.Request, body []byte) (*http.Response, error) {
		return nil, errors.New("cdn down")
	})
	st, _, accountID := setupAccount(t, ft)
	ms, _ := media.NewStore(t.TempDir())
	client := ilink.NewClient(ilink.Config{BaseURL: "https://ilink.test", Token: "test-bot-token", HTTP: ft.Client()})
	var mediaErrs atomic.Int32
	var gotText string
	done := make(chan struct{})
	sess, _ := session.New(withTestCDNFlags(session.Options{
		AccountID: accountID, Client: client, Store: st, HTTP: ft.Client(), MediaStore: ms,
		CDNBaseURL: "https://cdn-test.example/c2c",
		LongPollTimeout: 50 * time.Millisecond, RetryDelay: 10 * time.Millisecond,
		OnMediaError: func(msg session.InboundMessage, err error) { mediaErrs.Add(1) },
		Handler: func(ctx context.Context, msg session.InboundMessage) error {
			gotText = msg.Text
			close(done)
			return nil
		},
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sess.Run(ctx)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
	cancel()
	if gotText != "hello" || mediaErrs.Load() == 0 {
		t.Fatalf("text=%q mediaErrs=%d", gotText, mediaErrs.Load())
	}
}

// silence unused imports in case of build tags
var _ = io.EOF
