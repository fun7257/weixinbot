package session_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fun7257/weixinbot/internal/testutil"
	"github.com/fun7257/weixinbot/protocol"
	"github.com/fun7257/weixinbot/session"
	"github.com/fun7257/weixinbot/state"
)

func TestOpenWithBindsHandler(t *testing.T) {
	ft := testutil.NewFakeTransport()
	defaultCGI(ft)
	ft.Enqueue(protocol.PathGetUpdates, testutil.JSONResponder(200, protocol.GetUpdatesResp{
		Ret: 0, GetUpdatesBuf: "cursor-openwith",
		Msgs: []protocol.WeixinMessage{{
			FromUserID: "user@im.wechat", ContextToken: "ctx-ow",
			ItemList: []protocol.MessageItem{{Type: protocol.ItemTypeText, TextItem: &protocol.TextItem{Text: "hi"}}},
		}},
	}, nil))

	dir := t.TempDir()
	st, err := state.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	const accountID = "bot-ow"
	if err := st.RegisterAccountID(accountID); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveAccount(accountID, state.Account{Token: "test-bot-token", BaseURL: "https://ilink.test"}); err != nil {
		t.Fatal(err)
	}

	var bound *session.Session
	done := make(chan struct{})
	sess, err := session.OpenWith(st, accountID, ft.Client(), func(s *session.Session) session.Handler {
		bound = s
		return func(ctx context.Context, msg session.InboundMessage) error {
			if err := s.SendText(ctx, msg.FromUserID, "echo: "+msg.Text); err != nil {
				return err
			}
			close(done)
			return nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if bound != sess {
		t.Fatal("bind must receive the same *Session")
	}
	if sess.AccountID() != accountID {
		t.Fatalf("AccountID %q", sess.AccountID())
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- sess.Run(ctx) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for OpenWith handler")
	}
	cancel()
	<-errCh

	var sawEcho bool
	for _, p := range ft.Snapshot() {
		if !strings.Contains(p.URL, "sendmessage") {
			continue
		}
		var req protocol.SendMessageReq
		if err := testutil.DecodeJSON(p.Body, &req); err != nil {
			t.Fatal(err)
		}
		if req.Msg != nil && len(req.Msg.ItemList) > 0 && req.Msg.ItemList[0].TextItem != nil &&
			req.Msg.ItemList[0].TextItem.Text == "echo: hi" {
			sawEcho = true
		}
	}
	if !sawEcho {
		t.Fatal("expected SendText from bound handler")
	}
}

func TestOpenWithNilBind(t *testing.T) {
	dir := t.TempDir()
	st, err := state.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterAccountID("bot-nil"); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveAccount("bot-nil", state.Account{Token: "t", BaseURL: "https://ilink.test"}); err != nil {
		t.Fatal(err)
	}
	sess, err := session.OpenWith(st, "bot-nil", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sess.AccountID() != "bot-nil" {
		t.Fatalf("%q", sess.AccountID())
	}
}
