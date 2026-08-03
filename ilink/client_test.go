package ilink_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/fun7257/weixinbot/ilink"
	"github.com/fun7257/weixinbot/protocol"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGetUpdatesHeadersAndBody(t *testing.T) {
	var saw *http.Request
	var body []byte
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		saw = req
		body, _ = io.ReadAll(req.Body)
		resp := protocol.GetUpdatesResp{Ret: 0, GetUpdatesBuf: "next", Msgs: nil}
		b, _ := json.Marshal(resp)
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(string(b))),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	})
	c := ilink.NewClient(ilink.Config{
		BaseURL: "https://ilink.example",
		Token:   "secret",
		HTTP:    &http.Client{Transport: rt},
	})
	_, err := c.GetUpdates(context.Background(), "prev-cursor", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(saw.URL.Path, protocol.PathGetUpdates) {
		t.Fatalf("path %s", saw.URL.Path)
	}
	if saw.Header.Get("AuthorizationType") != protocol.AuthorizationTypeILinkBotToken {
		t.Fatal("AuthorizationType")
	}
	if saw.Header.Get("Authorization") != "Bearer secret" {
		t.Fatalf("auth %q", saw.Header.Get("Authorization"))
	}
	if saw.Header.Get("X-WECHAT-UIN") == "" {
		t.Fatal("missing X-WECHAT-UIN")
	}
	var req protocol.GetUpdatesReq
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	if req.GetUpdatesBuf != "prev-cursor" {
		t.Fatalf("cursor %q", req.GetUpdatesBuf)
	}
	if req.BaseInfo == nil || req.BaseInfo.BotAgent == "" {
		t.Fatal("base_info required")
	}
}
