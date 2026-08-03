package session

import (
	"context"
	"math/rand"
	"sync"
	"time"

	"github.com/tencent-weixin/weixinbot/ilink"
)

const (
	configCacheTTL          = 24 * time.Hour
	configCacheInitialRetry = 2 * time.Second
	configCacheMaxRetry     = time.Hour
	// Jitter is up to 1/12 of TTL (~2h) so refresh is ~24h ± jitter.
	configCacheJitterFrac = 12
)

// ConfigCache caches getConfig typing_ticket per user with ~24h TTL plus
// small random jitter, and exponential backoff (cap 1h) on failure.
type ConfigCache struct {
	client *ilink.Client
	mu     sync.Mutex
	entries map[string]*configEntry
	// Now injectable for tests
	Now func() time.Time
	// RandFloat returns [0,1) for refresh jitter; nil uses math/rand
	RandFloat func() float64
}

type configEntry struct {
	typingTicket  string
	everSucceeded bool
	nextFetchAt   time.Time
	retryDelay    time.Duration
}

// NewConfigCache creates a cache bound to an iLink client.
func NewConfigCache(client *ilink.Client) *ConfigCache {
	return &ConfigCache{
		client:  client,
		entries: make(map[string]*configEntry),
		Now:     time.Now,
	}
}

func (c *ConfigCache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *ConfigCache) randFloat() float64 {
	if c.RandFloat != nil {
		return c.RandFloat()
	}
	return rand.Float64()
}

// TypingTicket returns a cached or freshly fetched typing ticket for userID.
func (c *ConfigCache) TypingTicket(ctx context.Context, userID, contextToken string) string {
	if c == nil || c.client == nil || userID == "" {
		return ""
	}
	now := c.now()
	c.mu.Lock()
	ent := c.entries[userID]
	need := ent == nil || !now.Before(ent.nextFetchAt)
	c.mu.Unlock()
	if need {
		c.fetch(ctx, userID, contextToken)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[userID]; e != nil {
		return e.typingTicket
	}
	return ""
}

// nextSuccessRefresh returns now + TTL - jitter, jitter ∈ [0, TTL/12).
func (c *ConfigCache) nextSuccessRefresh(now time.Time) time.Time {
	jitterMax := configCacheTTL / configCacheJitterFrac
	jitter := time.Duration(c.randFloat() * float64(jitterMax))
	return now.Add(configCacheTTL - jitter)
}

func (c *ConfigCache) fetch(ctx context.Context, userID, contextToken string) {
	resp, err := c.client.GetConfig(ctx, userID, contextToken)
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	ent := c.entries[userID]
	if err == nil && resp != nil && resp.Ret == 0 {
		c.entries[userID] = &configEntry{
			typingTicket:  resp.TypingTicket,
			everSucceeded: true,
			nextFetchAt:   c.nextSuccessRefresh(now),
			retryDelay:    configCacheInitialRetry,
		}
		return
	}
	// failure backoff
	if ent == nil {
		c.entries[userID] = &configEntry{
			nextFetchAt: now.Add(configCacheInitialRetry),
			retryDelay:  configCacheInitialRetry,
		}
		return
	}
	next := ent.retryDelay * 2
	if next > configCacheMaxRetry {
		next = configCacheMaxRetry
	}
	if ent.retryDelay == 0 {
		ent.retryDelay = configCacheInitialRetry
		next = configCacheInitialRetry
	}
	ent.nextFetchAt = now.Add(next)
	ent.retryDelay = next
}
