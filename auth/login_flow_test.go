package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/fun7257/weixinbot/auth"
	"github.com/fun7257/weixinbot/internal/testutil"
	"github.com/fun7257/weixinbot/state"
)

func TestLoginQRPersistsAndOnQROnce(t *testing.T) {
	auth.ResetActiveLoginsForTest()
	ft := testutil.NewFakeTransport()
	ft.OnContains(auth.PathGetBotQRCode, testutil.JSONResponder(200, map[string]string{
		"qrcode": "qr-flow", "qrcode_img_content": "https://qr.example/flow",
	}, nil))
	ft.Enqueue(auth.PathGetQRCodeStatus,
		testutil.JSONResponder(200, map[string]string{"status": "wait"}, nil),
		testutil.JSONResponder(200, map[string]any{
			"status": "confirmed", "bot_token": "tok-flow",
			"ilink_bot_id": "bot-flow", "baseurl": "https://ilinkai.weixin.qq.com/",
			"ilink_user_id": "user-flow",
		}, nil),
	)
	dir := t.TempDir()
	st, err := state.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	var onQR int
	var seen auth.QRStart
	res, err := auth.LoginQR(context.Background(), st, auth.LoginQROptions{
		Options: auth.Options{
			BaseURL: "https://login.test", HTTP: ft.Client(),
			PollInterval: time.Millisecond, AllowAnyHost: true,
		},
		SessionKey: "flow-ok",
		OnQR: func(qr auth.QRStart) {
			onQR++
			seen = qr
		},
	})
	if err != nil || res == nil || !res.Connected || res.AccountID != "bot-flow" || res.BotToken != "tok-flow" {
		t.Fatalf("%+v %v", res, err)
	}
	if onQR != 1 {
		t.Fatalf("OnQR called %d times, want 1", onQR)
	}
	if seen.QRCodeURL != "https://qr.example/flow" || seen.SessionKey != "flow-ok" || seen.RawQRCode != "qr-flow" {
		t.Fatalf("OnQR start %+v", seen)
	}
	acc, err := st.LoadAccount("bot-flow")
	if err != nil || acc.Token != "tok-flow" || acc.UserID != "user-flow" {
		t.Fatalf("persisted %+v %v", acc, err)
	}
	ids, err := st.ListAccountIDs()
	if err != nil || len(ids) != 1 || ids[0] != "bot-flow" {
		t.Fatalf("ids %v %v", ids, err)
	}
}

func TestLoginQRWaitFailureDoesNotComplete(t *testing.T) {
	auth.ResetActiveLoginsForTest()
	ft := testutil.NewFakeTransport()
	ft.OnContains(auth.PathGetBotQRCode, testutil.JSONResponder(200, map[string]string{
		"qrcode": "qr", "qrcode_img_content": "https://qr",
	}, nil))
	ft.OnContains(auth.PathGetQRCodeStatus, testutil.JSONResponder(200, map[string]string{"status": "expired"}, nil))
	dir := t.TempDir()
	st, err := state.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	var onQR int
	res, err := auth.LoginQR(context.Background(), st, auth.LoginQROptions{
		Options: auth.Options{
			BaseURL: "https://login.test", HTTP: ft.Client(),
			MaxQRRefresh: 1, PollInterval: time.Millisecond, AllowAnyHost: true,
		},
		SessionKey: "flow-fail",
		OnQR: func(auth.QRStart) {
			onQR++
		},
	})
	if err == nil || (res != nil && res.Connected) {
		t.Fatalf("expected wait failure, got %+v %v", res, err)
	}
	if onQR != 1 {
		t.Fatalf("OnQR called %d times, want 1", onQR)
	}
	ids, err := st.ListAccountIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("CompleteLogin must not persist on WaitLogin failure, ids=%v", ids)
	}
	if _, loadErr := st.LoadAccount("bot-flow"); loadErr == nil {
		t.Fatal("unexpected persisted account")
	}
}

func TestLoginQRNilStore(t *testing.T) {
	_, err := auth.LoginQR(context.Background(), nil, auth.LoginQROptions{})
	if err == nil {
		t.Fatal("expected nil store error")
	}
}
