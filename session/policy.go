package session

import (
	"errors"
	"time"

	"github.com/fun7257/weixinbot/state"
)

// DefaultSessionWindow is the max age of last inbound for outbound replies.
const DefaultSessionWindow = 24 * time.Hour

// DefaultOutboundQuota is max outbound messages per inbound window.
const DefaultOutboundQuota = 10

// OutboundPolicy enforces hard blocks before any sendmessage call.
//
// Defaults are hard-on (24h window, quota 10, token required). Opt-out flags
// (Disable*, AllowMissingToken) are not recommended for production ClawBot limits.
type OutboundPolicy struct {
	// SessionWindow is max age since last inbound (default 24h). Zero still means default.
	// Set DisableSessionWindow to skip the time check (token still required unless disabled).
	SessionWindow time.Duration
	// OutboundQuota is max sends since last inbound (default 10).
	OutboundQuota int
	// DisableSessionWindow skips the 24h time check (token still required). Not recommended.
	DisableSessionWindow bool
	// DisableOutboundQuota skips the count check. Not recommended.
	DisableOutboundQuota bool
	// AllowMissingToken allows send without context_token (not recommended).
	AllowMissingToken bool
	// Now is optional clock for tests.
	Now func() time.Time
}

func (p OutboundPolicy) withDefaults() OutboundPolicy {
	if p.SessionWindow <= 0 {
		p.SessionWindow = DefaultSessionWindow
	}
	if p.OutboundQuota <= 0 {
		p.OutboundQuota = DefaultOutboundQuota
	}
	if p.Now == nil {
		p.Now = time.Now
	}
	return p
}

// Check returns ErrSessionWindow / ErrOutboundQuota or nil.
// Prefer TryReserveOutbound for send paths (atomic). This remains for read-only checks.
func (p OutboundPolicy) Check(peer state.PeerState, contextToken string) error {
	p = p.withDefaults()
	token := contextToken
	if token == "" {
		token = peer.ContextToken
	}
	if token == "" && !p.AllowMissingToken {
		return ErrSessionWindow
	}
	if !p.DisableSessionWindow {
		if peer.LastInboundAt.IsZero() {
			return ErrSessionWindow
		}
		if p.Now().Sub(peer.LastInboundAt) > p.SessionWindow {
			return ErrSessionWindow
		}
	}
	if !p.DisableOutboundQuota {
		if peer.OutboundCount >= p.OutboundQuota {
			return ErrOutboundQuota
		}
	}
	return nil
}

// reserveOpts maps policy to state.ReserveOpts for n slots.
func (p OutboundPolicy) reserveOpts(n int) state.ReserveOpts {
	p = p.withDefaults()
	return state.ReserveOpts{
		N:                    n,
		SessionWindow:        p.SessionWindow,
		OutboundQuota:        p.OutboundQuota,
		Now:                  p.Now(),
		DisableSessionWindow: p.DisableSessionWindow,
		DisableOutboundQuota: p.DisableOutboundQuota,
		AllowMissingToken:    p.AllowMissingToken,
	}
}

// mapReserveErr maps state reserve errors to session sentinels via errors.Is.
func mapReserveErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, state.ErrSessionWindow):
		return ErrSessionWindow
	case errors.Is(err, state.ErrOutboundQuota):
		return ErrOutboundQuota
	default:
		return err
	}
}
