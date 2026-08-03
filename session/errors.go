package session

import (
	"errors"
	"fmt"

	"github.com/fun7257/weixinbot/ilink"
)

// Sentinel errors for outbound policy hard blocks.
var (
	// ErrSessionWindow is returned when context_token is missing or the
	// inbound session window (default 24h) has expired. No sendmessage is issued.
	ErrSessionWindow = errors.New("session: session window closed (no context_token or last inbound too old)")

	// ErrOutboundQuota is returned when outbound count since last inbound
	// exceeds the configured limit (default 10). No sendmessage is issued.
	ErrOutboundQuota = errors.New("session: outbound quota exceeded since last inbound")
)

// UserMessageFromError maps library errors to short Chinese user-facing text.
// Policy errors are matched with errors.Is (including wrapped mapReserveErr results).
func UserMessageFromError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, ErrSessionWindow):
		return "会话已过期或尚未建立，请先向机器人发送一条消息后再试。"
	case errors.Is(err, ErrOutboundQuota):
		return "本轮回复条数已达上限，请再发一条消息后继续。"
	case ilink.IsRateLimited(err):
		return "发送过于频繁，请稍后再试。"
	case ilink.IsStaleToken(err):
		return "登录已失效，请重新扫码登录。"
	default:
		return fmt.Sprintf("发送失败：%v", err)
	}
}
