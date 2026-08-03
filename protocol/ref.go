package protocol

import (
	"bytes"
	"encoding/json"
	"strings"
)

// RefMessage is a quoted/referenced message on the wire (item.ref_msg).
//
// Production payloads vary: some only set title, some nest message_item, and
// some use alternate key names. UnmarshalJSON accepts known aliases and keeps
// RawJSON for diagnostics when the shape is unfamiliar.
type RefMessage struct {
	MessageItem *MessageItem `json:"message_item,omitempty"`
	Title       string       `json:"title,omitempty"`
	// RawJSON is the original ref_msg object bytes (not re-serialized).
	RawJSON string `json:"-"`
}

// UnmarshalJSON accepts multiple real-world key spellings for ref_msg.
func (r *RefMessage) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return nil
	}
	r.RawJSON = string(data)

	// Fast path: documented shape. Note: when message_item is an array (or other
	// mismatched type), encoding/json may leave a non-nil empty *MessageItem
	// even though Unmarshal returns an error — ignore empty results.
	type std struct {
		MessageItem *MessageItem `json:"message_item"`
		Title       string       `json:"title"`
	}
	var s std
	_ = json.Unmarshal(data, &s)
	if s.MessageItem != nil && !messageItemEmpty(s.MessageItem) {
		r.MessageItem = s.MessageItem
	}
	r.Title = strings.TrimSpace(s.Title)

	// Flexible map for aliases / odd shapes.
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		// Non-object (e.g. string) — treat as title.
		if str, ok := rawString(data); ok {
			r.Title = str
		}
		return nil
	}

	if r.Title == "" {
		r.Title = firstRawString(m,
			"title", "Title", "ref_title", "refTitle",
			"summary", "desc", "description", "content",
			"name", "nickname", "from_user_id", "fromUserId",
		)
	}

	if r.MessageItem == nil || messageItemEmpty(r.MessageItem) {
		for _, key := range []string{
			"message_item", "messageItem", "msg", "message",
			"item", "ref_item", "refItem", "quoted_message", "quotedMessage",
		} {
			raw, ok := m[key]
			if !ok || len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				continue
			}
			if mi, ok := parseAsMessageItem(raw); ok {
				r.MessageItem = mi
				break
			}
		}
	}

	// Still nothing typed: lift any nested text-looking string into Title.
	if r.Title == "" && r.MessageItem == nil {
		for k, raw := range m {
			if str, ok := rawString(raw); ok && str != "" {
				lk := strings.ToLower(k)
				if strings.Contains(lk, "text") || strings.Contains(lk, "body") ||
					strings.Contains(lk, "title") || strings.Contains(lk, "content") {
					r.Title = str
					break
				}
			}
		}
	}
	if r.Title == "" && r.MessageItem == nil {
		for _, raw := range m {
			if str, ok := rawString(raw); ok && str != "" {
				r.Title = str
				break
			}
		}
	}
	return nil
}

func parseAsMessageItem(raw json.RawMessage) (*MessageItem, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, false
	}
	// Array of items → first (check before object: some payloads nest lists).
	if raw[0] == '[' {
		var arr []MessageItem
		if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
			mi := arr[0]
			return &mi, true
		}
		return nil, false
	}
	// Plain string → text item
	if raw[0] == '"' {
		if str, ok := rawString(raw); ok && str != "" {
			return &MessageItem{
				Type:     ItemTypeText,
				TextItem: &TextItem{Text: str},
			}, true
		}
		return nil, false
	}
	// Object MessageItem
	var mi MessageItem
	if err := json.Unmarshal(raw, &mi); err == nil {
		if !messageItemEmpty(&mi) {
			return &mi, true
		}
	}
	// Full WeixinMessage envelope (has item_list)
	var wm WeixinMessage
	if err := json.Unmarshal(raw, &wm); err == nil && len(wm.ItemList) > 0 {
		return &wm.ItemList[0], true
	}
	return nil, false
}

func messageItemEmpty(mi *MessageItem) bool {
	if mi == nil {
		return true
	}
	// Production quote shells often only carry msg_id + timestamps (type=0).
	if strings.TrimSpace(mi.MsgID) != "" {
		return false
	}
	if mi.CreateTimeMs != 0 || mi.UpdateTimeMs != 0 {
		return false
	}
	return mi.Type == 0 && mi.TextItem == nil && mi.ImageItem == nil &&
		mi.VoiceItem == nil && mi.FileItem == nil && mi.VideoItem == nil &&
		mi.RefMsg == nil && mi.ToolCallStartItem == nil && mi.ToolCallResultItem == nil
}

func firstRawString(m map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		if raw, ok := m[k]; ok {
			if s, ok := rawString(raw); ok {
				return s
			}
		}
	}
	return ""
}

func rawString(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s), s != ""
	}
	// number → string
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String(), true
	}
	return "", false
}
