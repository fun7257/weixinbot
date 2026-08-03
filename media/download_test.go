package media_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/fun7257/weixinbot/internal/testutil"
	"github.com/fun7257/weixinbot/media"
	"github.com/fun7257/weixinbot/protocol"
)

// TestDownloadPrefersServerFullURL ensures inbound full_url is used even when
// its host is not the configured CDN base (production openclaw-weixin behavior).
func TestDownloadPrefersServerFullURL(t *testing.T) {
	key := []byte("0123456789abcdef")
	plain := []byte("full-url-plaintext!!")
	ct, err := media.EncryptAES128ECB(plain, key)
	if err != nil {
		t.Fatal(err)
	}
	hexKey := hex.EncodeToString(key)

	ft := testutil.NewFakeTransport()
	// full_url path is /dl/token — only served when full_url is used as-is.
	ft.OnContains("/dl/", testutil.BytesResponder(200, ct, nil))
	// Built-from-param path would be /c2c/download?encrypted_query_param=...
	ft.OnContains("/c2c/download", testutil.BytesResponder(500, []byte("wrong host"), nil))

	cdn := &media.CDN{
		BaseURL: "https://novac2c.cdn.weixin.qq.com/c2c",
		HTTP:    ft.Client(),
	}
	store, err := media.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lm, err := media.DownloadItem(context.Background(), protocol.MessageItem{
		Type: protocol.ItemTypeImage,
		ImageItem: &protocol.ImageItem{
			AESKey: hexKey,
			Media: &protocol.CDNMedia{
				FullURL:           "https://szims.weixin.qq.com/dl/token-abc",
				EncryptQueryParam: "should-not-use-alone",
				EncryptType:       1,
			},
		},
	}, media.DownloadDeps{CDN: cdn, Store: store, AccountID: "bot1"})
	if err != nil {
		t.Fatal(err)
	}
	if lm == nil || lm.Kind != "image" {
		t.Fatalf("%+v", lm)
	}
	got, _ := os.ReadFile(lm.Path)
	if !bytes.Equal(got, plain) {
		t.Fatalf("content mismatch")
	}
	if ft.CountPath("/dl/") < 1 {
		t.Fatal("expected download via server full_url path")
	}
	if ft.CountPath("/c2c/download") > 0 {
		t.Fatal("must not rebuild download against CDN base when full_url present")
	}
}

func TestDownloadItemFourKinds(t *testing.T) {
	key := []byte("0123456789abcdef")
	plain := []byte("hello-media-plaintext!!")
	ct, err := media.EncryptAES128ECB(plain, key)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString(key)
	hexKey := hex.EncodeToString(key)

	ft := testutil.NewFakeTransport()
	ft.OnContains("/download", testutil.BytesResponder(200, ct, nil))
	// also match full_url host paths
	ft.Default = testutil.BytesResponder(200, ct, nil)

	cdn := &media.CDN{BaseURL: "https://cdn.test/c2c", HTTP: ft.Client(), AllowAnyFullURL: true}
	store, err := media.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		item protocol.MessageItem
		kind string
		mime string
	}{
		{
			name: "image",
			item: protocol.MessageItem{Type: protocol.ItemTypeImage, ImageItem: &protocol.ImageItem{
				AESKey: hexKey,
				Media:  &protocol.CDNMedia{FullURL: "https://cdn.test/download?x=1", AESKey: b64},
			}},
			kind: "image",
			mime: "image/jpeg",
		},
		{
			name: "file",
			item: protocol.MessageItem{Type: protocol.ItemTypeFile, FileItem: &protocol.FileItem{
				FileName: "doc.pdf",
				// Production file path: base64(ASCII hex key), not base64(raw 16).
				Media: &protocol.CDNMedia{EncryptQueryParam: "q", AESKey: base64.StdEncoding.EncodeToString([]byte(hexKey))},
			}},
			kind: "file",
			mime: "application/pdf",
		},
		{
			name: "video",
			item: protocol.MessageItem{Type: protocol.ItemTypeVideo, VideoItem: &protocol.VideoItem{
				Media: &protocol.CDNMedia{FullURL: "https://cdn.test/download", AESKey: b64},
			}},
			kind: "video",
			mime: "video/mp4",
		},
		{
			name: "voice-degrade-silk",
			item: protocol.MessageItem{Type: protocol.ItemTypeVoice, VoiceItem: &protocol.VoiceItem{
				Media: &protocol.CDNMedia{FullURL: "https://cdn.test/download", AESKey: b64},
				Text:  "asr text",
			}},
			kind: "voice",
			mime: "audio/silk",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := media.DownloadDeps{CDN: cdn, Store: store}
			lm, err := media.DownloadItem(context.Background(), tc.item, deps)
			if err != nil {
				t.Fatal(err)
			}
			if lm == nil || lm.Kind != tc.kind || lm.MIME != tc.mime {
				t.Fatalf("%+v", lm)
			}
			got, err := os.ReadFile(lm.Path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, plain) {
				t.Fatalf("plaintext mismatch path=%s", lm.Path)
			}
		})
	}
}

func TestDownloadVoiceTranscodeSuccess(t *testing.T) {
	key := []byte("0123456789abcdef")
	plain := []byte("#!SILK_V3fake")
	ct, _ := media.EncryptAES128ECB(plain, key)
	b64 := base64.StdEncoding.EncodeToString(key)
	ft := testutil.NewFakeTransport()
	ft.Default = testutil.BytesResponder(200, ct, nil)
	cdn := &media.CDN{BaseURL: "https://cdn.test/c2c", HTTP: ft.Client(), AllowAnyFullURL: true}
	store, _ := media.NewStore(t.TempDir())
	wav := media.PCMToWAV([]byte{0, 0, 1, 0}, media.SILKSampleRate)
	lm, err := media.DownloadItem(context.Background(), protocol.MessageItem{
		Type:      protocol.ItemTypeVoice,
		VoiceItem: &protocol.VoiceItem{Media: &protocol.CDNMedia{FullURL: "https://cdn.test/d", AESKey: b64}},
	}, media.DownloadDeps{
		CDN: cdn, Store: store,
		SilkToWAV: func(silk []byte) ([]byte, error) { return wav, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if lm.MIME != "audio/wav" {
		t.Fatal(lm.MIME)
	}
}

func TestDownloadMissingURLAndMaxBytes(t *testing.T) {
	store, _ := media.NewStore(t.TempDir())
	store.MaxBytes = 4
	cdn := &media.CDN{BaseURL: "https://cdn.test/c2c", HTTP: http.DefaultClient}
	_, err := media.DownloadItem(context.Background(), protocol.MessageItem{
		Type:      protocol.ItemTypeImage,
		ImageItem: &protocol.ImageItem{Media: &protocol.CDNMedia{}},
	}, media.DownloadDeps{CDN: cdn, Store: store})
	if err == nil {
		t.Fatal("expected missing url")
	}
	// max bytes via Save
	_, err = store.Save("a", "x.bin", []byte("12345"))
	if err == nil {
		t.Fatal("expected max bytes")
	}
}

func TestSilkHelpers(t *testing.T) {
	pcm := []byte{0, 1, 2, 3}
	wav := media.PCMToWAV(pcm, 24000)
	out, sr, err := media.WAVToPCM(wav)
	if err != nil || sr != 24000 || !bytes.Equal(out, pcm) {
		t.Fatalf("%v %d %v", err, sr, out)
	}
	if !media.IsSilk([]byte("#!SILK_V3xxx")) {
		t.Fatal("header")
	}
	silk, err := media.WAVOrPCMToSilk(append([]byte("#!SILK_V3"), 1, 2), nil)
	if err != nil || !media.IsSilk(silk) {
		t.Fatal(err)
	}
	// wav without encoder fails
	if _, err := media.WAVOrPCMToSilk(wav, media.NilSilkCodec{}); err == nil {
		t.Fatal("expected encoder error")
	}
	_ = filepath.Join // silence
}

func TestDefaultSilkCodecRoundTrip(t *testing.T) {
	// Uses DefaultSilkCodec (RealSilkCodec non-race; FramedPCMSilkCodec under -race).
	sr := media.SILKSampleRate
	n := sr / 5 * 2 // 200ms * 2 bytes
	pcm := make([]byte, n)
	for i := 0; i < len(pcm); i += 2 {
		if (i/2)%40 < 20 {
			pcm[i] = 0x00
			pcm[i+1] = 0x10
		}
	}
	codec := media.DefaultSilkCodec
	silk, err := codec.EncodePCMToSilk(pcm, sr)
	if err != nil {
		t.Fatal(err)
	}
	if !media.IsSilk(silk) {
		t.Fatalf("not silk header: %q", silk[:min(12, len(silk))])
	}
	out, outSR, err := codec.DecodeSilkToPCM(silk)
	if err != nil {
		t.Fatal(err)
	}
	if outSR != sr {
		t.Fatalf("sr %d", outSR)
	}
	if len(out) < len(pcm)/4 {
		t.Fatalf("decoded pcm too short: %d", len(out))
	}
	wav, err := media.SilkToWAV(silk, codec)
	if err != nil || len(wav) < 44 {
		t.Fatalf("SilkToWAV %v len=%d", err, len(wav))
	}
}

func TestFramedPCMSilkCodecExactRoundTrip(t *testing.T) {
	pcm := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	c := media.FramedPCMSilkCodec{}
	silk, err := c.EncodePCMToSilk(pcm, 24000)
	if err != nil {
		t.Fatal(err)
	}
	out, sr, err := c.DecodeSilkToPCM(silk)
	if err != nil {
		t.Fatal(err)
	}
	if sr != 24000 || !bytes.Equal(out, pcm) {
		t.Fatalf("sr=%d out=%v", sr, out)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestImageBadKeyFailsClosed(t *testing.T) {
	ft := testutil.NewFakeTransport()
	ft.Default = testutil.BytesResponder(200, []byte("cipher"), nil)
	cdn := &media.CDN{BaseURL: "https://cdn.test/c2c", HTTP: ft.Client(), AllowAnyFullURL: true}
	store, _ := media.NewStore(t.TempDir())
	_, err := media.DownloadItem(context.Background(), protocol.MessageItem{
		Type: protocol.ItemTypeImage,
		ImageItem: &protocol.ImageItem{
			AESKey: "not-hex",
			Media:  &protocol.CDNMedia{FullURL: "https://cdn.test/download"},
		},
	}, media.DownloadDeps{CDN: cdn, Store: store})
	if err == nil {
		t.Fatal("expected key error")
	}
}

func TestDetectMediaKind(t *testing.T) {
	dir := t.TempDir()
	png := filepath.Join(dir, "a.png")
	_ = os.WriteFile(png, []byte{0x89, 0x50, 0x4e, 0x47}, 0o600)
	if media.DetectMediaKind(png) != "image" {
		t.Fatal("png")
	}
	if media.DetectMediaKind(filepath.Join(dir, "x.unknown")) != "file" {
		// missing file falls through to file when mime unknown
	}
	if media.DetectMediaKind("v.mp4") != "video" {
		t.Fatal("mp4")
	}
	if media.DetectMediaKind("a.silk") != "voice" {
		t.Fatal("silk")
	}
}
