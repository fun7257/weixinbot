package testutil_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tencent-weixin/weixinbot/internal/testutil"
)

func TestFakeTransportQueueOrder(t *testing.T) {
	ft := testutil.NewFakeTransport()
	ft.EnqueueJSON("getupdates", 200, map[string]any{"ret": 0, "n": 1})
	ft.EnqueueJSON("getupdates", 200, map[string]any{"ret": 0, "n": 2})
	c := ft.Client()

	for want := 1; want <= 2; want++ {
		req, _ := http.NewRequest(http.MethodPost, "https://ilink.test/ilink/bot/getupdates", strings.NewReader("{}"))
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if !strings.Contains(string(b), `"n":`+itoa(want)) {
			t.Fatalf("want n=%d body=%s", want, b)
		}
	}
	if len(ft.Snapshot()) != 2 {
		t.Fatalf("captured %d", len(ft.Snapshot()))
	}
}

func TestFakeTransportUnknownPath404(t *testing.T) {
	ft := testutil.NewFakeTransport()
	req, _ := http.NewRequest(http.MethodPost, "https://ilink.test/unknown", nil)
	res, err := ft.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 404 {
		t.Fatalf("status %d", res.StatusCode)
	}
}

func TestAssertBotAuth(t *testing.T) {
	h := make(http.Header)
	h.Set("AuthorizationType", "ilink_bot_token")
	h.Set("Authorization", "Bearer secret")
	h.Set("X-WECHAT-UIN", "abc")
	if err := testutil.AssertBotAuth(h); err != nil {
		t.Fatal(err)
	}
	h.Set("Authorization", "nope")
	if err := testutil.AssertBotAuth(h); err == nil {
		t.Fatal("expected auth error")
	}
}

func TestDecodeJSON(t *testing.T) {
	var m map[string]int
	if err := testutil.DecodeJSON([]byte(`{"a":1}`), &m); err != nil {
		t.Fatal(err)
	}
	if m["a"] != 1 {
		t.Fatalf("%v", m)
	}
}

func itoa(n int) string {
	if n == 1 {
		return "1"
	}
	return "2"
}
