package auth_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fun7257/weixinbot/auth"
	"github.com/fun7257/weixinbot/internal/testutil"
	"github.com/fun7257/weixinbot/state"
)

func TestWaitLoginConfirmed(t *testing.T) {
	auth.ResetActiveLoginsForTest()
	ft := testutil.NewFakeTransport()
	ft.OnContains(auth.PathGetBotQRCode, testutil.JSONResponder(200, map[string]string{
		"qrcode": "qr-1", "qrcode_img_content": "https://qr.example/1",
	}, nil))
	ft.Enqueue(auth.PathGetQRCodeStatus,
		testutil.JSONResponder(200, map[string]string{"status": "wait"}, nil),
		testutil.JSONResponder(200, map[string]any{
			"status": "confirmed", "bot_token": "tok-1",
			"ilink_bot_id": "bot-abc", "baseurl": "https://ilinkai.weixin.qq.com/",
			"ilink_user_id": "user-1",
		}, nil),
	)
	opts := auth.Options{BaseURL: "https://login.test", HTTP: ft.Client(), PollInterval: time.Millisecond, AllowAnyHost: true}
	start, err := auth.StartQR(context.Background(), "s1", opts)
	if err != nil {
		t.Fatal(err)
	}
	if start.QRCodeURL == "" || start.RawQRCode != "qr-1" {
		t.Fatalf("%+v", start)
	}
	res, err := auth.WaitLogin(context.Background(), "s1", opts)
	if err != nil || !res.Connected || res.BotToken != "tok-1" || res.AccountID != "bot-abc" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestExpiredMaxRefresh(t *testing.T) {
	auth.ResetActiveLoginsForTest()
	ft := testutil.NewFakeTransport()
	// many QR fetches + expired statuses
	ft.OnContains(auth.PathGetBotQRCode, testutil.JSONResponder(200, map[string]string{
		"qrcode": "qr", "qrcode_img_content": "https://qr",
	}, nil))
	ft.OnContains(auth.PathGetQRCodeStatus, testutil.JSONResponder(200, map[string]string{"status": "expired"}, nil))
	opts := auth.Options{BaseURL: "https://login.test", HTTP: ft.Client(), MaxQRRefresh: 1, PollInterval: time.Millisecond, AllowAnyHost: true}
	_, _ = auth.StartQR(context.Background(), "s2", opts)
	res, err := auth.WaitLogin(context.Background(), "s2", opts)
	if err == nil || res.Connected {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestNeedVerifyCode(t *testing.T) {
	auth.ResetActiveLoginsForTest()
	ft := testutil.NewFakeTransport()
	ft.OnContains(auth.PathGetBotQRCode, testutil.JSONResponder(200, map[string]string{
		"qrcode": "qr", "qrcode_img_content": "https://qr",
	}, nil))
	ft.Enqueue(auth.PathGetQRCodeStatus,
		testutil.JSONResponder(200, map[string]string{"status": "need_verifycode"}, nil),
		testutil.JSONResponder(200, map[string]any{
			"status": "confirmed", "bot_token": "t", "ilink_bot_id": "b1", "ilink_user_id": "u",
		}, nil),
	)
	opts := auth.Options{
		BaseURL: "https://login.test", HTTP: ft.Client(), PollInterval: time.Millisecond, AllowAnyHost: true,
		VerifyCode: func(ctx context.Context, prompt string) (string, error) {
			return "1234", nil
		},
	}
	_, _ = auth.StartQR(context.Background(), "s3", opts)
	res, err := auth.WaitLogin(context.Background(), "s3", opts)
	if err != nil || !res.Connected {
		t.Fatalf("%+v %v", res, err)
	}
	// verify_code present on a status poll
	var sawVerify bool
	for _, c := range ft.Snapshot() {
		if strings.Contains(c.URL, "verify_code=1234") {
			sawVerify = true
		}
	}
	if !sawVerify {
		t.Fatal("expected verify_code query")
	}
}

func TestRedirectHost(t *testing.T) {
	auth.ResetActiveLoginsForTest()
	ft := testutil.NewFakeTransport()
	ft.OnContains(auth.PathGetBotQRCode, testutil.JSONResponder(200, map[string]string{
		"qrcode": "qr", "qrcode_img_content": "https://qr",
	}, nil))
	var hosts []string
	ft.OnContains(auth.PathGetQRCodeStatus, func(req *http.Request, body []byte) (*http.Response, error) {
		hosts = append(hosts, req.URL.Host)
		if len(hosts) == 1 {
			return testutil.JSONResponder(200, map[string]string{
				"status": "scaned_but_redirect", "redirect_host": "idc2.weixin.qq.com",
			}, nil)(req, body)
		}
		return testutil.JSONResponder(200, map[string]any{
			"status": "confirmed", "bot_token": "t", "ilink_bot_id": "b",
		}, nil)(req, body)
	})
	// AllowAnyHost so initial login.test base works; redirect still validated via weixin allowlist.
	opts := auth.Options{BaseURL: "https://login.test", HTTP: ft.Client(), PollInterval: time.Millisecond, AllowAnyHost: true}
	_, _ = auth.StartQR(context.Background(), "s4", opts)
	res, err := auth.WaitLogin(context.Background(), "s4", opts)
	if err != nil || !res.Connected {
		t.Fatalf("%+v %v", res, err)
	}
	if len(hosts) < 2 || hosts[1] != "idc2.weixin.qq.com" {
		t.Fatalf("hosts %v", hosts)
	}
}

func TestBindedRedirect(t *testing.T) {
	auth.ResetActiveLoginsForTest()
	ft := testutil.NewFakeTransport()
	ft.OnContains(auth.PathGetBotQRCode, testutil.JSONResponder(200, map[string]string{
		"qrcode": "qr", "qrcode_img_content": "https://qr",
	}, nil))
	ft.OnContains(auth.PathGetQRCodeStatus, testutil.JSONResponder(200, map[string]string{"status": "binded_redirect"}, nil))
	opts := auth.Options{BaseURL: "https://login.test", HTTP: ft.Client(), AllowAnyHost: true}
	_, _ = auth.StartQR(context.Background(), "s5", opts)
	res, err := auth.WaitLogin(context.Background(), "s5", opts)
	if err != nil || !res.AlreadyConnected {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestCtxTimeout(t *testing.T) {
	auth.ResetActiveLoginsForTest()
	ft := testutil.NewFakeTransport()
	ft.OnContains(auth.PathGetBotQRCode, testutil.JSONResponder(200, map[string]string{
		"qrcode": "qr", "qrcode_img_content": "https://qr",
	}, nil))
	ft.OnContains(auth.PathGetQRCodeStatus, func(req *http.Request, body []byte) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	opts := auth.Options{BaseURL: "https://login.test", HTTP: ft.Client(), StatusTimeout: 20 * time.Millisecond, PollInterval: time.Millisecond, AllowAnyHost: true}
	_, _ = auth.StartQR(context.Background(), "s6", opts)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := auth.WaitLogin(ctx, "s6", opts)
	if err == nil {
		t.Fatal("expected timeout")
	}
}

func TestCompleteLogin(t *testing.T) {
	dir := t.TempDir()
	st, err := state.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.RegisterAccountID("old")
	_ = st.SaveAccount("old", state.Account{Token: "oldtok", UserID: "uid"})
	res := &auth.LoginResult{
		Connected: true, BotToken: "newtok", AccountID: "new-bot",
		BaseURL: "https://ilinkai.weixin.qq.com", UserID: "uid",
	}
	if err := auth.CompleteLogin(st, res); err != nil {
		t.Fatal(err)
	}
	acc, err := st.LoadAccount("new-bot")
	if err != nil || acc.Token != "newtok" || acc.UserID != "uid" {
		t.Fatalf("%+v %v", acc, err)
	}
	if _, err := st.LoadAccount("old"); err == nil {
		t.Fatal("old should be cleared")
	}
	// invalid account id
	if err := auth.CompleteLogin(st, &auth.LoginResult{Connected: true, BotToken: "t", AccountID: "../x"}); err == nil {
		t.Fatal("expected sanitize reject")
	}
}

func TestStartQRBodyLocalTokens(t *testing.T) {
	auth.ResetActiveLoginsForTest()
	ft := testutil.NewFakeTransport()
	ft.OnContains(auth.PathGetBotQRCode, testutil.JSONResponder(200, map[string]string{
		"qrcode": "qr", "qrcode_img_content": "https://qr",
	}, nil))
	_, err := auth.StartQR(context.Background(), "s7", auth.Options{
		BaseURL: "https://login.test", HTTP: ft.Client(), LocalTokens: []string{"a", "b"}, AllowAnyHost: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	_ = json.Unmarshal(ft.Snapshot()[0].Body, &body)
	list, _ := body["local_token_list"].([]any)
	if len(list) != 2 {
		t.Fatalf("%v", body)
	}
}
