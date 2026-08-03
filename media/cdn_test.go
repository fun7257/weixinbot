package media_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/fun7257/weixinbot/internal/testutil"
	"github.com/fun7257/weixinbot/media"
)

func TestUploadCiphertextRetriesThenSucceeds(t *testing.T) {
	ft := testutil.NewFakeTransport()
	var n atomic.Int32
	ft.OnContains("/upload", func(req *http.Request, body []byte) (*http.Response, error) {
		if n.Add(1) < 3 {
			return testutil.BytesResponder(503, []byte("try again"), nil)(req, body)
		}
		return testutil.BytesResponder(200, []byte("ok"), map[string]string{
			"x-encrypted-param": "dl-param-ok",
		})(req, body)
	})
	cdn := &media.CDN{BaseURL: "https://cdn.test/c2c", HTTP: ft.Client(), AllowAnyFullURL: true}
	param, err := cdn.UploadCiphertext(context.Background(), "https://cdn.test/upload", "up", "fk", []byte("cipher"))
	if err != nil {
		t.Fatal(err)
	}
	if param != "dl-param-ok" {
		t.Fatalf("param %q", param)
	}
	if n.Load() != 3 {
		t.Fatalf("attempts %d", n.Load())
	}
}

func TestUploadCiphertextNoRetryOn4xx(t *testing.T) {
	ft := testutil.NewFakeTransport()
	var n atomic.Int32
	ft.OnContains("/upload", func(req *http.Request, body []byte) (*http.Response, error) {
		n.Add(1)
		return testutil.BytesResponder(403, []byte("forbidden"), nil)(req, body)
	})
	cdn := &media.CDN{BaseURL: "https://cdn.test/c2c", HTTP: ft.Client(), AllowAnyFullURL: true}
	_, err := cdn.UploadCiphertext(context.Background(), "https://cdn.test/upload", "", "fk", []byte("x"))
	if err == nil {
		t.Fatal("expected error")
	}
	if n.Load() != 1 {
		t.Fatalf("4xx must not retry, attempts=%d", n.Load())
	}
}
