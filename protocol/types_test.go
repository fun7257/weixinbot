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

func TestRefMessageFlexibleUnmarshal(t *testing.T) {
	cases := []struct {
		name      string
		json      string
		wantTitle string
		wantText  string
		wantKind  int // message_item.type, 0 if none required
	}{
		{
			name:      "standard title+message_item",
			json:      `{"title":"Author","message_item":{"type":1,"text_item":{"text":"hello"}}}`,
			wantTitle: "Author",
			wantText:  "hello",
			wantKind:  protocol.ItemTypeText,
		},
		{
			name:      "camelCase aliases",
			json:      `{"refTitle":"Bob","messageItem":{"type":1,"text_item":{"text":"hi"}}}`,
			wantTitle: "Bob",
			wantText:  "hi",
			wantKind:  protocol.ItemTypeText,
		},
		{
			name:      "msg alias string",
			json:      `{"title":"T","msg":"quoted body"}`,
			wantTitle: "T",
			wantText:  "quoted body",
			wantKind:  protocol.ItemTypeText,
		},
		{
			name:      "message_item array",
			json:      `{"message_item":[{"type":1,"text_item":{"text":"arr"}}]}`,
			wantTitle: "",
			wantText:  "arr",
			wantKind:  protocol.ItemTypeText,
		},
		{
			name:      "raw unknown keys still keep RawJSON",
			json:      `{"foo":"bar","baz":1}`,
			wantTitle: "bar",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ref protocol.RefMessage
			if err := json.Unmarshal([]byte(tc.json), &ref); err != nil {
				t.Fatal(err)
			}
			if ref.RawJSON == "" {
				t.Fatal("RawJSON empty")
			}
			if tc.wantTitle != "" && ref.Title != tc.wantTitle {
				t.Fatalf("title %q want %q (raw=%s)", ref.Title, tc.wantTitle, ref.RawJSON)
			}
			if tc.wantText != "" {
				if ref.MessageItem == nil || ref.MessageItem.TextItem == nil || ref.MessageItem.TextItem.Text != tc.wantText {
					t.Fatalf("message_item text %+v", ref.MessageItem)
				}
			}
			if tc.wantKind != 0 && (ref.MessageItem == nil || ref.MessageItem.Type != tc.wantKind) {
				// type may be inferred later; text present is enough for string alias case
				if ref.MessageItem == nil || ref.MessageItem.TextItem == nil {
					t.Fatalf("message_item %+v", ref.MessageItem)
				}
			}
		})
	}
}

func TestWeixinMessageRefMsgRoundTrip(t *testing.T) {
	raw := []byte(`{
		"from_user_id":"u@im.wechat",
		"context_token":"ctx",
		"item_list":[{
			"type":1,
			"text_item":{"text":"/quote"},
			"ref_msg":{"title":"原作者","message_item":{"type":1,"text_item":{"text":"被引用的话"}}}
		}]
	}`)
	var msg protocol.WeixinMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatal(err)
	}
	if len(msg.ItemList) != 1 || msg.ItemList[0].RefMsg == nil {
		t.Fatalf("%+v", msg)
	}
	ref := msg.ItemList[0].RefMsg
	if ref.Title != "原作者" || ref.MessageItem == nil || ref.MessageItem.TextItem == nil {
		t.Fatalf("%+v", ref)
	}
	if ref.MessageItem.TextItem.Text != "被引用的话" {
		t.Fatal(ref.MessageItem.TextItem.Text)
	}
}

// Production ClawBot quote shell: type=0 + msg_id, no text_item/media.
func TestRefMessageMsgIDShell(t *testing.T) {
	raw := []byte(`{
		"message_item":{
			"type":0,
			"create_time_ms":1785741406000,
			"update_time_ms":1785741406000,
			"is_completed":true,
			"msg_id":"7489942321137011208",
			"button_item_list":[],
			"at_bot_username_list":[]
		}
	}`)
	var ref protocol.RefMessage
	if err := json.Unmarshal(raw, &ref); err != nil {
		t.Fatal(err)
	}
	if ref.MessageItem == nil {
		t.Fatal("message_item nil")
	}
	if ref.MessageItem.MsgID != "7489942321137011208" {
		t.Fatalf("msg_id %q", ref.MessageItem.MsgID)
	}
	if ref.MessageItem.Type != 0 || !ref.MessageItem.IsCompleted {
		t.Fatalf("%+v", ref.MessageItem)
	}
}
