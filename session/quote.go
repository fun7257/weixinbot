package session

import (
	"fmt"
	"strings"

	"github.com/fun7257/weixinbot/media"
	"github.com/fun7257/weixinbot/protocol"
)

// Quote kinds after parseQuote classification.
const (
	QuoteKindText    = "text" // title and/or embedded text_item
	QuoteKindImage   = "image"
	QuoteKindVoice   = "voice"
	QuoteKindFile    = "file"
	QuoteKindVideo   = "video"
	QuoteKindMsgID   = "msgid"   // production ClawBot: type=0 shell + msg_id only
	QuoteKindUnknown = "unknown" // non-empty ref_msg we could not classify
)

// Quote is a parsed item.ref_msg (user-quoted message).
//
// Wire shapes (observed / openclaw-compatible):
//
//  1. Text quote: title and/or message_item with text_item
//  2. Media quote: message_item type image|voice|file|video
//  3. MsgID shell (ClawBot production): message_item { type:0, msg_id, timestamps }
//     — no embedded body; callers that need preview must keep their own history
//
// This package does not cache messages; it only classifies the wire payload.
type Quote struct {
	// Title is server summary when present (sender name / short preview).
	Title string
	// Text is embedded body when present (text / ASR / file name).
	// For msgid shells, Text is set to MsgID for convenience (same id, not body content).
	Text string
	// Kind is one of QuoteKind* constants.
	Kind string
	// MsgID is ref_msg.message_item.msg_id when set (msgid shell or any item with id).
	MsgID string
	// Media is a kind-only marker for media quotes (Path empty unless caller downloads).
	Media *media.LocalMedia
	// Raw is the wire message_item when present.
	Raw *protocol.MessageItem
	// RawJSON is the original ref_msg object.
	RawJSON string
}

// HasContent reports whether the quote carries anything useful for agents.
func (q *Quote) HasContent() bool {
	if q == nil {
		return false
	}
	return strings.TrimSpace(q.Title) != "" ||
		strings.TrimSpace(q.Text) != "" ||
		strings.TrimSpace(q.MsgID) != "" ||
		q.Kind != "" ||
		q.Media != nil
}

// IsMedia reports a media quote (image/voice/file/video).
func (q *Quote) IsMedia() bool {
	if q == nil {
		return false
	}
	switch q.Kind {
	case QuoteKindImage, QuoteKindVoice, QuoteKindFile, QuoteKindVideo:
		return true
	}
	return false
}

// IsMsgIDShell reports production type=0 + msg_id only references (no embedded body).
func (q *Quote) IsMsgIDShell() bool {
	return q != nil && q.Kind == QuoteKindMsgID && strings.TrimSpace(q.MsgID) != ""
}

// Summary is a short one-line description for logs / Body().
func (q *Quote) Summary() string {
	if q == nil {
		return ""
	}
	if q.IsMedia() {
		if q.Title != "" {
			return q.Title
		}
		return "[" + q.Kind + "]"
	}
	if q.IsMsgIDShell() {
		return "msg_id=" + strings.TrimSpace(q.MsgID)
	}
	parts := make([]string, 0, 2)
	if t := strings.TrimSpace(q.Title); t != "" && t != "[引用消息]" {
		parts = append(parts, t)
	}
	if t := strings.TrimSpace(q.Text); t != "" && t != q.MsgID {
		parts = append(parts, t)
	}
	if len(parts) > 0 {
		return strings.Join(parts, " | ")
	}
	if q.MsgID != "" {
		return "msg_id=" + q.MsgID
	}
	if t := strings.TrimSpace(q.Text); t != "" {
		return t
	}
	return ""
}

// FormatBody builds agent-facing text (openclaw-weixin-inspired):
//   - media quote → current text only (media is Quote.Kind / Media)
//   - text / msgid quote → "[引用: summary]\n" + text
func FormatBody(text string, q *Quote) string {
	if q == nil {
		return text
	}
	if q.IsMedia() {
		return text
	}
	sum := q.Summary()
	if sum == "" {
		return text
	}
	prefix := "[引用: " + sum + "]"
	if text == "" {
		return prefix
	}
	return prefix + "\n" + text
}

// parseQuote finds the first ref_msg on any item (usually the TEXT reply item).
func parseQuote(items []protocol.MessageItem) *Quote {
	for i := range items {
		if items[i].RefMsg == nil {
			continue
		}
		return quoteFromRef(items[i].RefMsg)
	}
	return nil
}

func quoteFromRef(ref *protocol.RefMessage) *Quote {
	if ref == nil {
		return nil
	}
	q := &Quote{
		Title:   strings.TrimSpace(ref.Title),
		RawJSON: ref.RawJSON,
	}
	if ref.MessageItem != nil {
		mi := ref.MessageItem
		q.Raw = mi
		q.MsgID = strings.TrimSpace(mi.MsgID)

		typ := mi.Type
		if typ == 0 {
			typ = inferItemType(mi)
		}
		switch typ {
		case protocol.ItemTypeText:
			q.Kind = QuoteKindText
			if mi.TextItem != nil {
				q.Text = mi.TextItem.Text
			}
		case protocol.ItemTypeImage:
			q.Kind = QuoteKindImage
			q.Media = &media.LocalMedia{Kind: "image"}
			if q.Title == "" {
				q.Title = "[图片]"
			}
		case protocol.ItemTypeVoice:
			q.Kind = QuoteKindVoice
			q.Media = &media.LocalMedia{Kind: "voice"}
			if mi.VoiceItem != nil {
				q.Text = mi.VoiceItem.Text
			}
			if q.Title == "" {
				q.Title = "[语音]"
			}
		case protocol.ItemTypeFile:
			q.Kind = QuoteKindFile
			q.Media = &media.LocalMedia{Kind: "file"}
			if mi.FileItem != nil {
				q.Text = mi.FileItem.FileName
			}
			if q.Title == "" {
				q.Title = "[文件]"
			}
		case protocol.ItemTypeVideo:
			q.Kind = QuoteKindVideo
			q.Media = &media.LocalMedia{Kind: "video"}
			if q.Title == "" {
				q.Title = "[视频]"
			}
		default:
			if mi.TextItem != nil && mi.TextItem.Text != "" {
				q.Kind = QuoteKindText
				q.Text = mi.TextItem.Text
			} else if q.MsgID != "" {
				// Production ClawBot: type=0 shell + msg_id only (no text/media).
				q.Kind = QuoteKindMsgID
				if q.Title == "" {
					q.Title = "[引用消息]"
				}
				// Text mirrors MsgID so callers always have a non-empty field;
				// it is an id, not message body content.
				q.Text = q.MsgID
			}
		}
	}

	if !q.HasContent() {
		raw := strings.TrimSpace(q.RawJSON)
		if raw == "" || raw == "{}" || raw == "null" {
			return nil
		}
		q.Kind = QuoteKindUnknown
		q.Title = "[未识别引用结构]"
		q.Text = raw
		return q
	}
	// Title-only (no message_item) → text kind.
	if q.Kind == "" && q.Title != "" {
		q.Kind = QuoteKindText
	}
	return q
}

func inferItemType(mi *protocol.MessageItem) int {
	if mi == nil {
		return 0
	}
	switch {
	case mi.ImageItem != nil:
		return protocol.ItemTypeImage
	case mi.VoiceItem != nil:
		return protocol.ItemTypeVoice
	case mi.FileItem != nil:
		return protocol.ItemTypeFile
	case mi.VideoItem != nil:
		return protocol.ItemTypeVideo
	case mi.TextItem != nil:
		return protocol.ItemTypeText
	default:
		return 0
	}
}

func quoteDebug(q *Quote) string {
	if q == nil {
		return "nil"
	}
	return fmt.Sprintf("title=%q text=%q kind=%q msg_id=%q",
		q.Title, q.Text, q.Kind, q.MsgID)
}
