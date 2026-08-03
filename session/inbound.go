package session

import (
	"fmt"
	"strings"
	"time"

	"github.com/tencent-weixin/weixinbot/media"
	"github.com/tencent-weixin/weixinbot/protocol"
)

// Quote is a parsed ref_msg.
type Quote struct {
	Title string
	Text  string
	Media *media.LocalMedia // kind-only marker when media not downloaded for quote
}

// InboundMessage is a host-agnostic event after one getupdates item is decoded.
type InboundMessage struct {
	AccountID    string
	FromUserID   string
	ContextToken string
	Text         string
	MessageID    int64
	CreateTimeMs int64
	Quote        *Quote
	Media        []media.LocalMedia
	Items        []protocol.MessageItem
	Raw          protocol.WeixinMessage
	ReceivedAt   time.Time
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

// ParseInbound extracts text/quote fields without network I/O.
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
	if p.Quote != nil && p.Quote.Title != "" {
		// readable prefix for agents that only look at Text
		prefix := "「" + p.Quote.Title
		if p.Quote.Text != "" {
			prefix += ": " + p.Quote.Text
		}
		prefix += "」\n"
		if p.Text != "" {
			p.Text = prefix + p.Text
		} else {
			p.Text = strings.TrimSuffix(prefix, "\n")
		}
	}
	return p
}

func extractText(items []protocol.MessageItem) string {
	for _, it := range items {
		if it.Type == protocol.ItemTypeText && it.TextItem != nil {
			return it.TextItem.Text
		}
	}
	// ASR from voice
	for _, it := range items {
		if it.Type == protocol.ItemTypeVoice && it.VoiceItem != nil && it.VoiceItem.Text != "" {
			return it.VoiceItem.Text
		}
	}
	return ""
}

func parseQuote(items []protocol.MessageItem) *Quote {
	for _, it := range items {
		if it.RefMsg == nil {
			continue
		}
		q := &Quote{Title: it.RefMsg.Title}
		if it.RefMsg.MessageItem != nil {
			mi := it.RefMsg.MessageItem
			switch mi.Type {
			case protocol.ItemTypeText:
				if mi.TextItem != nil {
					q.Text = mi.TextItem.Text
				}
			case protocol.ItemTypeImage:
				q.Media = &media.LocalMedia{Kind: "image"}
			case protocol.ItemTypeVoice:
				q.Media = &media.LocalMedia{Kind: "voice"}
				if mi.VoiceItem != nil {
					q.Text = mi.VoiceItem.Text
				}
			case protocol.ItemTypeFile:
				q.Media = &media.LocalMedia{Kind: "file"}
				if mi.FileItem != nil {
					q.Text = mi.FileItem.FileName
				}
			case protocol.ItemTypeVideo:
				q.Media = &media.LocalMedia{Kind: "video"}
			}
		}
		return q
	}
	return nil
}

// FormatInboundDebug is a small helper for debug logs.
func FormatInboundDebug(in InboundMessage) string {
	return fmt.Sprintf("from=%s text=%q media=%d", in.FromUserID, in.Text, len(in.Media))
}
