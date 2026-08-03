package ilink_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fun7257/weixinbot/ilink"
	"github.com/fun7257/weixinbot/internal/testutil"
	"github.com/fun7257/weixinbot/protocol"
)

func newTestClient(ft *testutil.FakeTransport) *ilink.Client {
	return ilink.NewClient(ilink.Config{
		BaseURL: "https://ilink.test",
		Token:   "tok-secret",
		HTTP:    ft.Client(),
		Timeout: 2 * time.Second,
	})
}

func TestSendMessageCGI(t *testing.T) {
	ft := testutil.NewFakeTransport()
	ft.EnqueueJSON(protocol.PathSendMessage, 200, protocol.SendMessageResp{Ret: 0})
	c := newTestClient(ft)
	err := c.SendMessage(context.Background(), &protocol.WeixinMessage{
		ToUserID:     "u",
		ContextToken: "ctx",
		MessageType:  protocol.MessageTypeBot,
		ItemList:     []protocol.MessageItem{{Type: 1, TextItem: &protocol.TextItem{Text: "hi"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cap := ft.Snapshot()
	if len(cap) != 1 {
		t.Fatal(len(cap))
	}
	if err := testutil.AssertBotAuth(cap[0].Header); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cap[0].URL, protocol.PathSendMessage) {
		t.Fatal(cap[0].URL)
	}
	var req protocol.SendMessageReq
	if err := testutil.DecodeJSON(cap[0].Body, &req); err != nil {
		t.Fatal(err)
	}
	if req.BaseInfo == nil || req.BaseInfo.BotAgent == "" {
		t.Fatal("base_info")
	}
	if req.Msg == nil || req.Msg.ToUserID != "u" {
		t.Fatal("msg")
	}
}

func TestSendMessageRetNonZero(t *testing.T) {
	ft := testutil.NewFakeTransport()
	ft.EnqueueJSON(protocol.PathSendMessage, 200, protocol.SendMessageResp{Ret: -2, ErrMsg: "rate limited"})
	c := newTestClient(ft)
	err := c.SendMessage(context.Background(), &protocol.WeixinMessage{ToUserID: "u"})
	if !ilink.IsRateLimited(err) {
		t.Fatalf("want rate limited, got %v", err)
	}
}

func TestSendMessageEmptyBodyIsSuccess(t *testing.T) {
	ft := testutil.NewFakeTransport()
	// Production iLink often returns 200 with empty body or only whitespace.
	ft.OnContains(protocol.PathSendMessage, func(req *http.Request, body []byte) (*http.Response, error) {
		return testutil.BytesResponder(200, []byte("  \n"), nil)(req, body)
	})
	c := newTestClient(ft)
	if err := c.SendMessage(context.Background(), &protocol.WeixinMessage{
		ToUserID: "u", ContextToken: "ctx", MessageType: 2, MessageState: 2,
		ItemList: []protocol.MessageItem{{Type: 1, TextItem: &protocol.TextItem{Text: "hi"}}},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSendMessageEmptyJSONObjectIsSuccess(t *testing.T) {
	ft := testutil.NewFakeTransport()
	ft.OnContains(protocol.PathSendMessage, testutil.BytesResponder(200, []byte("{}"), nil))
	c := newTestClient(ft)
	if err := c.SendMessage(context.Background(), &protocol.WeixinMessage{ToUserID: "u"}); err != nil {
		t.Fatal(err)
	}
}

func TestGetUploadURLCGI(t *testing.T) {
	ft := testutil.NewFakeTransport()
	ft.EnqueueJSON(protocol.PathGetUploadURL, 200, protocol.GetUploadURLResp{UploadFullURL: "https://cdn/u"})
	c := newTestClient(ft)
	resp, err := c.GetUploadURL(context.Background(), &protocol.GetUploadURLReq{
		FileKey: "fk", MediaType: 1, ToUserID: "u", NoNeedThumb: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.UploadFullURL != "https://cdn/u" {
		t.Fatal(resp)
	}
	cap := ft.Snapshot()[0]
	if err := testutil.AssertBotAuth(cap.Header); err != nil {
		t.Fatal(err)
	}
	var req protocol.GetUploadURLReq
	_ = json.Unmarshal(cap.Body, &req)
	if !req.NoNeedThumb || req.BaseInfo == nil {
		t.Fatalf("%+v", req)
	}
}

func TestGetConfigCGI(t *testing.T) {
	ft := testutil.NewFakeTransport()
	ft.EnqueueJSON(protocol.PathGetConfig, 200, protocol.GetConfigResp{Ret: 0, TypingTicket: "tt-1"})
	c := newTestClient(ft)
	resp, err := c.GetConfig(context.Background(), "user1", "ctx")
	if err != nil {
		t.Fatal(err)
	}
	if resp.TypingTicket != "tt-1" {
		t.Fatal(resp)
	}
	var req protocol.GetConfigReq
	_ = json.Unmarshal(ft.Snapshot()[0].Body, &req)
	if req.ILinkUserID != "user1" || req.ContextToken != "ctx" || req.BaseInfo == nil {
		t.Fatalf("%+v", req)
	}
}

func TestGetConfigRetError(t *testing.T) {
	ft := testutil.NewFakeTransport()
	ft.EnqueueJSON(protocol.PathGetConfig, 200, protocol.GetConfigResp{Ret: 1, ErrMsg: "fail"})
	c := newTestClient(ft)
	_, err := c.GetConfig(context.Background(), "u", "")
	var ae *ilink.APIError
	if !errors.As(err, &ae) || ae.Ret != 1 {
		t.Fatalf("%v", err)
	}
}

func TestSendTypingCGI(t *testing.T) {
	ft := testutil.NewFakeTransport()
	ft.EnqueueJSON(protocol.PathSendTyping, 200, map[string]any{"ret": 0})
	c := newTestClient(ft)
	if err := c.SendTyping(context.Background(), "u", "ticket", protocol.TypingStatusTyping); err != nil {
		t.Fatal(err)
	}
	var req protocol.SendTypingReq
	_ = json.Unmarshal(ft.Snapshot()[0].Body, &req)
	if req.Status != 1 || req.TypingTicket != "ticket" || req.BaseInfo == nil {
		t.Fatalf("%+v", req)
	}
}

func TestNotifyStartStopCGI(t *testing.T) {
	ft := testutil.NewFakeTransport()
	ft.EnqueueJSON(protocol.PathNotifyStart, 200, protocol.NotifyResp{Ret: 0})
	ft.EnqueueJSON(protocol.PathNotifyStop, 200, protocol.NotifyResp{Ret: 0})
	c := newTestClient(ft)
	if _, err := c.NotifyStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.NotifyStop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ft.CountPath(protocol.PathNotifyStart) != 1 || ft.CountPath(protocol.PathNotifyStop) != 1 {
		t.Fatal("notify counts")
	}
}

func TestGetUpdatesContextCancel(t *testing.T) {
	ft := testutil.NewFakeTransport()
	// hang until context cancels
	ft.OnContains(protocol.PathGetUpdates, func(req *http.Request, body []byte) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	c := newTestClient(ft)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := c.GetUpdates(ctx, "", 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) &&
		!strings.Contains(err.Error(), "context") {
		t.Fatalf("want context error, got %v", err)
	}
}

func TestGetUpdatesPathAndAuth(t *testing.T) {
	ft := testutil.NewFakeTransport()
	ft.EnqueueJSON(protocol.PathGetUpdates, 200, protocol.GetUpdatesResp{Ret: 0, GetUpdatesBuf: "n"})
	c := newTestClient(ft)
	_, err := c.GetUpdates(context.Background(), "prev", 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	cap := ft.Snapshot()[0]
	if !strings.Contains(cap.URL, protocol.PathGetUpdates) {
		t.Fatal(cap.URL)
	}
	if err := testutil.AssertBotAuth(cap.Header); err != nil {
		t.Fatal(err)
	}
	var req protocol.GetUpdatesReq
	_ = json.Unmarshal(cap.Body, &req)
	if req.GetUpdatesBuf != "prev" || req.BaseInfo == nil {
		t.Fatalf("%+v", req)
	}
}
