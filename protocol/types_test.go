package protocol_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tencent-weixin/weixinbot/protocol"
)

func TestPathConstantsStable(t *testing.T) {
	// Locked against accidental renames that would break production CGI.
	want := map[string]string{
		"getupdates":   "ilink/bot/getupdates",
		"sendmessage":  "ilink/bot/sendmessage",
		"getuploadurl": "ilink/bot/getuploadurl",
		"getconfig":    "ilink/bot/getconfig",
		"sendtyping":   "ilink/bot/sendtyping",
		"notifystart":  "ilink/bot/msg/notifystart",
		"notifystop":   "ilink/bot/msg/notifystop",
	}
	got := map[string]string{
		"getupdates":   protocol.PathGetUpdates,
		"sendmessage":  protocol.PathSendMessage,
		"getuploadurl": protocol.PathGetUploadURL,
		"getconfig":    protocol.PathGetConfig,
		"sendtyping":   protocol.PathSendTyping,
		"notifystart":  protocol.PathNotifyStart,
		"notifystop":   protocol.PathNotifyStop,
	}
	for k, w := range want {
		if got[k] != w {
			t.Fatalf("%s: got %q want %q", k, got[k], w)
		}
	}
	if protocol.AuthorizationTypeILinkBotToken != "ilink_bot_token" {
		t.Fatal("AuthorizationType")
	}
	if protocol.ItemTypeToolCallStart != 11 || protocol.ItemTypeToolCallResult != 12 {
		t.Fatal("tool item types")
	}
}

func TestFixtureRoundTrip(t *testing.T) {
	files := []string{
		"weixin_message_text.json",
		"weixin_message_image.json",
		"get_updates_resp.json",
		"send_message_req.json",
		"get_upload_url_req.json",
	}
	for _, name := range files {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			// Generic map round-trip preserves all keys present in fixture.
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			// Typed round-trip for message shapes.
			switch name {
			case "weixin_message_text.json", "weixin_message_image.json":
				var msg protocol.WeixinMessage
				if err := json.Unmarshal(raw, &msg); err != nil {
					t.Fatal(err)
				}
				out, err := json.Marshal(msg)
				if err != nil {
					t.Fatal(err)
				}
				var again protocol.WeixinMessage
				if err := json.Unmarshal(out, &again); err != nil {
					t.Fatal(err)
				}
				if again.FromUserID == "" || again.ContextToken == "" {
					t.Fatalf("lost fields: %+v", again)
				}
				if len(again.ItemList) == 0 {
					t.Fatal("lost item_list")
				}
			case "get_updates_resp.json":
				var resp protocol.GetUpdatesResp
				if err := json.Unmarshal(raw, &resp); err != nil {
					t.Fatal(err)
				}
				if resp.GetUpdatesBuf == "" || len(resp.Msgs) == 0 {
					t.Fatalf("%+v", resp)
				}
			case "send_message_req.json":
				var req protocol.SendMessageReq
				if err := json.Unmarshal(raw, &req); err != nil {
					t.Fatal(err)
				}
				if req.Msg == nil || req.Msg.ToUserID == "" {
					t.Fatalf("%+v", req)
				}
			case "get_upload_url_req.json":
				var req protocol.GetUploadURLReq
				if err := json.Unmarshal(raw, &req); err != nil {
					t.Fatal(err)
				}
				if !req.NoNeedThumb || req.MediaType == 0 {
					t.Fatalf("%+v", req)
				}
			}
		})
	}
}
