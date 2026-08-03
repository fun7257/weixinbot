package session

import (
	"fmt"
	"time"

	"github.com/fun7257/weixinbot/media"
	"github.com/fun7257/weixinbot/protocol"
)

// InboundMessage is a host-agnostic event after one getupdates item is decoded.
type InboundMessage struct {
	AccountID    string
	FromUserID   string
	ContextToken string
	// Text is the current message body only (no quote prefix), so slash commands stay intact.
	Text         string
	MessageID    int64
	CreateTimeMs int64
	Quote        *Quote
	Media        []media.LocalMedia
	// MediaErrors lists per-item download failures; the message is still delivered.
	MediaErrors []string
	Items       []protocol.MessageItem
	Raw         protocol.WeixinMessage
	ReceivedAt  time.Time
}

// Body builds agent-facing text: FormatBody(Text, Quote).
// Prefer Text for command routing; Body for LLM / display with quote context.
func (in InboundMessage) Body() string {
	return FormatBody(in.Text, in.Quote)
}

// ParsedInbound is parse-only result without downloaded media paths.
type ParsedInbound struct {
	FromUserID   string
	ContextToken string
	Text         string
	MessageID    int64
	CreateTimeMs int64
	Quote        *Quote
	Items        []protocol.MessageItem
	Raw          protocol.WeixinMessage
}

// Body is FormatBody(Text, Quote).
func (p ParsedInbound) Body() string {
	return FormatBody(p.Text, p.Quote)
}

// ParseInbound extracts text/quote fields without network I/O.
//
// Quote model (see quote.go):
//   - Text: user content only (no quote prefix — slash commands stay intact)
//   - Quote: structured ref_msg (text / media / msgid shell)
//   - Body(): display string with "[引用: …]" when applicable
//
// This package does not cache history. MsgID shells only expose msg_id;
// preview of the quoted body is the caller's responsibility if needed.
func ParseInbound(raw protocol.WeixinMessage) ParsedInbound {
	p := ParsedInbound{
		FromUserID:   raw.FromUserID,
		ContextToken: raw.ContextToken,
		MessageID:    raw.MessageID,
		CreateTimeMs: raw.CreateTimeMs,
		Items:        raw.ItemList,
		Raw:          raw,
	}
	p.Text = extractText(raw.ItemList)
	p.Quote = parseQuote(raw.ItemList)
	return p
}

func extractText(items []protocol.MessageItem) string {
	for _, it := range items {
		if it.Type == protocol.ItemTypeText && it.TextItem != nil {
			return it.TextItem.Text
		}
		// Some payloads omit type but still fill text_item.
		if it.Type == 0 && it.TextItem != nil && it.TextItem.Text != "" &&
			it.ImageItem == nil && it.VoiceItem == nil {
			return it.TextItem.Text
		}
	}
	for _, it := range items {
		if it.Type == protocol.ItemTypeVoice && it.VoiceItem != nil && it.VoiceItem.Text != "" {
			return it.VoiceItem.Text
		}
	}
	return ""
}

// FormatInboundDebug is a small helper for debug logs.
func FormatInboundDebug(in InboundMessage) string {
	return fmt.Sprintf("from=%s text=%q body=%q media=%d quote={%s} mediaErrs=%d",
		in.FromUserID, in.Text, in.Body(), len(in.Media), quoteDebug(in.Quote), len(in.MediaErrors))
}
