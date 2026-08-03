# weixinbot API Reference

> **Language:** [English](./API.en.md) | [中文](./API.md)

Module: `github.com/tencent-weixin/weixinbot`  
Role: **WeChat iLink Bot communication library** (not an Agent, OpenClaw plugin, or webhook server).  
Go: `1.22+` (see `go.mod`).

```text
WeChat user ──iLink HTTP + CDN──► weixinbot ──► your Agent / Handler
```

| Direction | Mechanism |
|-----------|-----------|
| Inbound | Client long-polls `POST ilink/bot/getupdates` with `get_updates_buf` cursor |
| Outbound | Client `POST ilink/bot/sendmessage`, echoing `context_token` |
| Media | `getuploadurl` → AES-128-ECB CDN upload → `sendmessage` with CDN refs |

| Default | Value |
|---------|-------|
| Base | `https://ilinkai.weixin.qq.com` |
| CDN | `https://novac2c.cdn.weixin.qq.com/c2c` |
| Session window | **24h** since last user inbound |
| Outbound quota | **10** messages per window |

---

## Table of contents

1. [Install](#1-install)
2. [Recommended call path](#2-recommended-call-path-90-of-apps)
3. [Packages and import graph](#3-packages-and-import-graph)
4. [`session` — primary API](#4-session--primary-api)
5. [`auth` — QR login](#5-auth--qr-login)
6. [`state` — persistence](#6-state--persistence)
7. [`ilink` — low-level CGI](#7-ilink--low-level-cgi)
8. [Wire protocol](#8-wire-protocol-requests-responses-types)
9. [`media` pipeline](#9-media-pipeline)
10. [`markdown`](#10-markdown)
11. [`protocol` constants](#11-protocol-constants-cheatsheet)
12. [Environment variables](#12-environment-variables-conventions)
13. [Error handling](#13-error-handling-checklist)
14. [Test injection](#14-test-injection)
15. [Call checklist](#15-call-checklist)
16. [Related docs](#16-related-docs)

---

## 1. Install

```bash
go get github.com/tencent-weixin/weixinbot@latest
```

Local development (example / monorepo):

```go
// go.mod
replace github.com/tencent-weixin/weixinbot => ../weixinbot
```

```go
import (
    "github.com/tencent-weixin/weixinbot/auth"
    "github.com/tencent-weixin/weixinbot/ilink"
    "github.com/tencent-weixin/weixinbot/session"
    "github.com/tencent-weixin/weixinbot/state"
    // optional: media, markdown, protocol
)
```

Default voice codec dependency: `github.com/wdvxdr1123/go-silk` (pure Go, no CGO).

---

## 2. Recommended call path (90% of apps)

```text
state.NewStore → login or SaveAccount
       ↓
session.New / session.Open
       ↓
Handler(InboundMessage) calls sess.Send*
       ↓
sess.Run(ctx)   // blocks until cancel; best-effort notifyStop on exit
```

### 2.1 Minimal runnable example

```go
package main

import (
    "context"
    "log"
    "os"
    "os/signal"
    "path/filepath"
    "syscall"

    "github.com/tencent-weixin/weixinbot/ilink"
    "github.com/tencent-weixin/weixinbot/session"
    "github.com/tencent-weixin/weixinbot/state"
)

func main() {
    stateDir := filepath.Join(os.Getenv("HOME"), ".weixinbot")
    store, err := state.NewStore(stateDir)
    if err != nil {
        log.Fatal(err)
    }
    accountID := "my-bot"
    _ = store.RegisterAccountID(accountID)
    _ = store.SaveAccount(accountID, state.Account{
        Token:   os.Getenv("ILINK_BOT_TOKEN"), // or after auth QR login
        BaseURL: ilink.DefaultBaseURL,
    })

    var sess *session.Session
    sess, err = session.Open(store, accountID, nil, func(ctx context.Context, msg session.InboundMessage) error {
        // Route commands with msg.Text; show models msg.Body() (includes quote summary)
        return sess.WithTyping(ctx, msg.FromUserID, func(ctx context.Context) error {
            return sess.SendText(ctx, msg.FromUserID, "echo: "+msg.Text)
        })
    })
    if err != nil {
        log.Fatal(err)
    }

    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()
    if err := sess.Run(ctx); err != nil && err != context.Canceled {
        log.Fatal(err)
    }
}
```

### 2.2 QR login then run

```go
start, err := auth.StartQR(ctx, "cli", auth.Options{
    VerifyCode: auth.StdinVerifyCode(os.Stdin, os.Stderr), // when verify code required
})
// Show start.QRCodeURL for the user to scan
res, err := auth.WaitLogin(ctx, start.SessionKey, auth.Options{/* same */})
_ = auth.CompleteLogin(store, res)
// Then Open + Run; accountID may be res.AccountID
```

### 2.3 End-to-end sequence (text)

```text
[App]  Run(ctx)
  │
  ├─► NotifyStart
  │
  └─► loop:
        GetUpdates(buf)  ──long poll──►  iLink
              │
              ├─ msgs[] → TouchInbound(token) → download media → Handler
              │                │
              │                └─ Handler: SendText / SendImage…
              │                       │
              │                       ├─ TryReserveOutbound (window/quota)
              │                       └─ SendMessage(msg + context_token)
              │
              └─ SaveSyncBuf(new buf)
  cancel → NotifyStop (best effort)
```

### 2.4 End-to-end sequence (outbound media)

```text
SendImageFile / SendFileAttachment / …
  │
  ├─ Policy.Check + TryReserveOutbound
  ├─ optional: caption → SendText chunks
  ├─ media.UploadFile:
  │     read file → random aeskey/filekey
  │     GetUploadURL(rawsize, md5, aeskey hex, no_need_thumb)
  │     AES-128-ECB encrypt → CDN PUT/POST
  └─ SendMessage(item + media.encrypt_query_param + media.aes_key)
```

---

## 3. Packages and import graph

| Package | Role | Typical caller |
|---------|------|----------------|
| `session` | Run loop, inbound parse, Send*, policy, typing, multi-account | **App entrypoint** |
| `auth` | QR login state machine | First bind / switch account |
| `state` | Credentials, cursor, context_token, peer quota | Persistence |
| `ilink` | HTTP CGI + error classification | Advanced / custom loops |
| `media` | AES/CDN/SILK/MIME | Advanced media pipelines |
| `markdown` | Outbound text filter + rune chunking | Custom pre-send |
| `protocol` | Wire types and path constants | Extension / debug |

Import direction (**no reverse imports**):

```text
protocol ← ilink, media, session, auth
state    ← auth, session
ilink    ← auth, media, session
media    ← session
markdown ← session
```

---

## 4. `session` — primary API

### 4.1 Construction

| API | Signature (summary) | Notes |
|-----|---------------------|-------|
| `session.New` | `New(opts Options) (*Session, error)` | Full config; does **not** start polling |
| `session.Open` | `Open(store, accountID, httpDoer, handler)` | Load account from store then `New`; `httpDoer` may be nil |
| `(*Session).Run` | `Run(ctx) error` | Long-poll until cancel; `NotifyStart` / `NotifyStop` inside |

`Open` ≈ `LoadAccount` → `ilink.NewClient` → `New(Options{...})`.

### 4.2 Full `Options` fields

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| `AccountID` | string | **required** | Local account id (path-sanitized) |
| `Client` | `*ilink.Client` | **required** | iLink control plane |
| `Store` | `*state.Store` | **required** | Credentials / peer / cursor |
| `CDNBaseURL` | string | `ilink.DefaultCDNBaseURL` | Media data plane |
| `HTTP` | `ilink.Doer` | nil | Inject fake (shared CGI + CDN) |
| `Handler` | `Handler` | nil | Per-inbound **sequential** callback |
| `Logger` | `*slog.Logger` | `slog.Default()` | |
| `LongPollTimeout` | `time.Duration` | 35s | Client hold for getupdates |
| `RetryDelay` | `time.Duration` | 2s | Backoff after transient poll errors |
| `StaleTokenPause` | `time.Duration` | 1h | Pause on ret/errcode=-14 |
| `Policy` | `OutboundPolicy` | 24h / 10 | Hard outbound policy |
| `TextChunkLimit` | int | 4000 | Text chunk size in **runes** |
| `RetryRateLimit` | int | 0 | Retries on sendmessage ret=-2 |
| `RateLimitBackoff` | `time.Duration` | 200ms | Initial rate-limit backoff |
| `MediaStore` | `*media.Store` | temp under Store | Inbound media on disk |
| `SilkCodec` | `media.SilkCodec` | `DefaultSilkCodec` | Voice codec |
| `OnHandlerError` | func | slog | When Handler returns error |
| `OnMediaError` | func | slog | Inbound media download failed (**message still delivered**) |
| `MaxMediaDownloadBytes` | int64 | 100MiB | Inbound download cap |
| `MaxMediaURLBytes` | int64 | 50MiB | Cap for `SendMediaURL` |
| `AllowAnyCDNFullURL` | bool | **false** | Tests only: relax CDN host pin |
| `MediaURLSkipDNSCheck` | bool | **false** | Tests only: skip SSRF DNS check |
| `TypingKeepalive` | `time.Duration` | 5s | `WithTyping` interval |
| `Now` | `func() time.Time` | `time.Now` | Test clock |

```go
type Handler func(ctx context.Context, msg InboundMessage) error
```

### 4.3 Inbound: `InboundMessage`

| Field / method | Type | Use |
|----------------|------|-----|
| `AccountID` | string | This session’s account |
| `FromUserID` | string | Sender; outbound `toUserID` |
| `ContextToken` | string | Session glue (auto-stored and echoed) |
| `Text` | string | **Current body only** (no quote prefix; good for `/command`) |
| `Body()` | string | For models/logs: `[引用: …]\n` + Text |
| `Quote` | `*Quote` | Structured quote; see §4.4 |
| `Media` | `[]media.LocalMedia` | Downloaded/decrypted local files |
| `MediaErrors` | `[]string` | Per-item media failures (callback still runs) |
| `Items` | `[]protocol.MessageItem` | Raw item_list |
| `Raw` | `protocol.WeixinMessage` | Full wire message |
| `MessageID` | int64 | Metadata |
| `CreateTimeMs` | int64 | Metadata |
| `ReceivedAt` | `time.Time` | Local receive time |

`media.LocalMedia`:

| Field | Notes |
|-------|-------|
| `Kind` | `image` \| `voice` \| `file` \| `video` |
| `Path` | Local absolute path |
| `MIME` | e.g. `image/png`, `audio/wav`, `audio/silk` |
| `FileName` | Inbound filename (files, etc.) |

```go
func handler(ctx context.Context, msg session.InboundMessage) error {
    if strings.HasPrefix(strings.TrimSpace(msg.Text), "/") {
        // command routing — use Text, not Body()
    }
    _ = msg.Body() // show to LLM
    for _, m := range msg.Media {
        // m.Kind / m.Path / m.MIME
    }
    if len(msg.MediaErrors) > 0 {
        // partial media failure; text still usable
    }
    return sess.SendText(ctx, msg.FromUserID, "ok")
}
```

Parse without network (tests / custom poll):

```go
parsed := session.ParseInbound(raw) // Text / Quote / Body; no media download
```

Debug helper: `session.FormatInboundDebug(msg)`.

### 4.4 Quote

| Kind constant | Value | Meaning | `Body()` behavior |
|---------------|-------|---------|-------------------|
| `QuoteKindText` | `text` | title / text_item | `[引用: title \| text]\n…` |
| `QuoteKindImage` etc. | `image`/`voice`/`file`/`video` | Media quote | Current Text only (media on Quote) |
| `QuoteKindMsgID` | `msgid` | **Production common**: `type=0` + `msg_id` only | `[引用: msg_id=…]\n…` |
| `QuoteKindUnknown` | `unknown` | Non-empty but unrecognized | Includes RawJSON for diagnostics |

`Quote` fields:

| Field | Notes |
|-------|-------|
| `Title` | Server summary (sender name / short preview) |
| `Text` | Embedded body; for msgid shells equals MsgID (**not** original body) |
| `Kind` | See table above |
| `MsgID` | `msg_id` inside `ref_msg` |
| `Media` | Media quote marker (Path empty by default) |
| `Raw` | Wire `message_item` |
| `RawJSON` | Original `ref_msg` JSON |

Helpers: `HasContent()` / `IsMedia()` / `IsMsgIDShell()` / `Summary()`.

**Important:**

- The protocol does **not** send the quoted body for msgid shells.
- This library does **not** keep a message history cache.
- To restore quoted text, the app should cache inbound messages keyed by `Quote.MsgID` (e.g. in-memory LRU).

```go
if msg.Quote != nil {
    switch msg.Quote.Kind {
    case session.QuoteKindMsgID:
        // msg.Quote.MsgID — look up app history
    case session.QuoteKindImage:
        // user quoted an image
    }
}
```

### 4.5 Outbound Send*

Every Send* runs policy checks **before** calling `sendmessage` (missing token / past 24h / over quota → error, **no network**).  
After policy passes: `TryReserveOutbound`; on send failure, `ReleaseOutbound` rolls back the reservation.

| API | Notes |
|-----|-------|
| `SendText(ctx, to, text)` | Markdown filter on by default + 4000-rune chunks; **each chunk counts as 1 quota** |
| `SendTextOpts(ctx, to, text, opts)` | `SendTextOptions{SkipMarkdownFilter: true}` |
| `SendItem(ctx, to, item)` | Raw `protocol.MessageItem` |
| `SendToolStart(ctx, to, name, callID)` | type=11 tool start |
| `SendToolResult(ctx, to, name, callID, status)` | type=12 tool result |
| `SendImageFile(ctx, to, path, caption)` | Upload + send image; caption may send as text first |
| `SendVideoFile(ctx, to, path, caption)` | Video |
| `SendFileAttachment(ctx, to, path)` | File attachment |
| `SendVoice(ctx, to, path)` | `.silk` pass-through; WAV/PCM → SILK then upload |
| `SendMedia(ctx, to, path, caption)` | Route by extension |
| `SendMediaURL(ctx, to, url, caption)` | Fetch public URL then send (SSRF checks) |

```go
_ = sess.SendText(ctx, to, "hello")
_ = sess.SendTextOpts(ctx, to, "**raw**", session.SendTextOptions{SkipMarkdownFilter: true})

_ = sess.SendImageFile(ctx, to, "/path/a.png", "caption")
_ = sess.SendFileAttachment(ctx, to, "/path/report.pdf")
_ = sess.SendVoice(ctx, to, "/path/a.wav")
_ = sess.SendMedia(ctx, to, "/path/clip.mp4", "")
_ = sess.SendMediaURL(ctx, to, "https://example.com/a.png", "")

_ = sess.SendToolStart(ctx, to, "search", "call-1")
_ = sess.SendToolResult(ctx, to, "search", "call-1", "completed")
```

Helpers:

| API | Notes |
|-----|-------|
| `StartTyping` / `StopTyping` | Typing indicator (uses cached `typing_ticket` from getconfig) |
| `WithTyping(ctx, userID, fn)` | Keepalive typing while `fn` runs |
| `Peer(userID)` | Read `state.PeerState` |
| `ContextToken(userID)` | Current token |
| `AccountID` / `Client` / `Store` | Access lower layers |

`ConfigCache` (internal to session): per-user cache of getconfig `typing_ticket`, ~24h TTL + jitter, exponential backoff on failure (cap 1h). Apps rarely use it directly.

### 4.6 Outbound policy

| Condition | Error | `UserMessageFromError` (zh UX strings) |
|-----------|-------|----------------------------------------|
| No `context_token`, or last inbound older than window | `session.ErrSessionWindow` | Session expired… |
| Outbound count ≥ quota in this window | `session.ErrOutboundQuota` | Reply quota reached… |
| ret=-2 rate limited | `ilink.IsRateLimited` | Sending too fast… |
| ret/errcode=-14 | `ilink.IsStaleToken` | Login expired… |

```go
type OutboundPolicy struct {
    SessionWindow time.Duration // 0 → DefaultSessionWindow (24h)
    OutboundQuota int           // 0 → DefaultOutboundQuota (10)
}
```

Hard defaults are **not** meant to be “turned off” in production (zero values apply library defaults rather than disabling).

```go
sess, _ = session.New(session.Options{
    // ...
    Policy: session.OutboundPolicy{
        SessionWindow: 24 * time.Hour,
        OutboundQuota: 10,
    },
    RetryRateLimit: 1, // optional: one rate-limit retry
})
```

Another user inbound → `TouchInbound` → **outbound counter reset**, window refreshed.

### 4.7 Multi-account `Manager`

```go
mgr := session.NewManager()
_ = mgr.Start(ctx, sessA)
_ = mgr.Start(ctx, sessB)
_ = mgr.Stop("account-a")
mgr.StopAll()
list := mgr.List()
s, ok := mgr.Get("account-a")
```

Each account has its own `Session` and credentials in Store; the caller picks which Session to send from (no implicit global bot).

---

## 5. `auth` — QR login

| API | Notes |
|-----|-------|
| `StartQR(ctx, sessionKey, opts)` | Fetch QR; returns `QRStart{QRCodeURL, SessionKey, RawQRCode}` |
| `WaitLogin(ctx, sessionKey, opts)` | Poll until success / failure / cancel |
| `CompleteLogin(store, result)` | Persist token / baseURL / userID; `RegisterAccountID` |
| `StdinVerifyCode(r, w)` | Terminal verify-code prompt |

### 5.1 `Options`

| Field | Default | Notes |
|-------|---------|-------|
| `BaseURL` | `FixedLoginBaseURL` (same default host as ilink) | Login API origin |
| `BotType` | `"3"` | |
| `HTTP` | default client | Injectable fake |
| `LocalTokens` | nil | `local_token_list` |
| `VerifyCode` | nil | Required for need_verifycode flows |
| `MaxQRRefresh` | 3 | Max QR refresh count |
| `PollInterval` | ~1s | Status poll interval |
| `StatusTimeout` | 35s | Per status request timeout |
| `AllowAnyHost` | false | **Tests only** |
| `AllowedHostSuffixes` | weixin.qq.com / qq.com | Redirect allowlist |

### 5.2 `LoginResult`

| Field | Notes |
|-------|-------|
| `Connected` / `AlreadyConnected` | Fresh connect / already connected |
| `BotToken` | Bearer token |
| `AccountID` | Prefer as store account id (ilink_bot_id) |
| `BaseURL` | Business base |
| `UserID` | WeChat-side user id |
| `Message` | Extra text |

```go
opts := auth.Options{
    BaseURL:      auth.FixedLoginBaseURL,
    PollInterval: time.Second,
    VerifyCode:   auth.StdinVerifyCode(os.Stdin, os.Stderr),
}
start, _ := auth.StartQR(ctx, "cli", opts)
fmt.Println(start.QRCodeURL)
res, err := auth.WaitLogin(ctx, start.SessionKey, opts)
if err == nil && res.Connected {
    _ = auth.CompleteLogin(store, res)
}
```

Tests: `ResetActiveLoginsForTest()`; never enable `AllowAnyHost` in production.

---

## 6. `state` — persistence

### 6.1 Layout

```text
$STATE_ROOT/
  accounts.json                         # account id index
  accounts/{id}.json                    # Account
  accounts/{id}.sync.json               # get_updates_buf
  accounts/{id}.context-tokens.json     # userID → token (implementation detail)
  accounts/{id}.peers.json              # peer state (implementation detail)
```

### 6.2 `Account`

| Field | JSON | Notes |
|-------|------|-------|
| `Token` | `token` | Bot Bearer |
| `BaseURL` | `baseUrl` | iLink origin |
| `UserID` | `userId` | Optional |
| `CDNBaseURL` | `cdnBaseUrl` | Optional CDN override |

### 6.3 API

| API | Notes |
|-----|-------|
| `NewStore(root)` | Create root dir |
| `RegisterAccountID` / `ListAccountIDs` | Account index |
| `SaveAccount` / `LoadAccount` | Credentials |
| `DeleteAccount` | Delete account files |
| `LoadSyncBuf` / `SaveSyncBuf` | Long-poll cursor |
| `SetContextToken` / `GetContextToken` / `RestoreContextTokens` | Session tokens |
| `ResolveContextToken` | Resolve current token |
| `TouchInbound` | Inbound: update token, time, **zero** outbound count |
| `GetPeer` | `PeerState{ContextToken, LastInboundAt, OutboundCount}` |
| `IncrOutbound` | Simple +1 (Send path prefers Reserve) |
| `TryReserveOutbound` | Atomic outbound quota reserve (used by Send) |
| `ReleaseOutbound` | Roll back reservation on failure |
| `ClearStaleAccountsForUserID` | Drop old accounts for same userId |

`accountID` is path-sanitized (no `..`, `/`, etc.).

```go
type PeerState struct {
    ContextToken  string
    LastInboundAt time.Time
    OutboundCount int
}

type ReserveOpts struct {
    N             int
    SessionWindow time.Duration
    OutboundQuota int
    Now           time.Time
}
```

---

## 7. `ilink` — low-level CGI

Prefer `session` for apps. Use Client for custom loops or debugging.

### 7.1 Construction

```go
client := ilink.NewClient(ilink.Config{
    BaseURL:        ilink.DefaultBaseURL,
    Token:          token,
    ChannelVersion: "1.0.0",
    BotAgent:       "MyBot/1.0",
    ClientVersion:  ilink.BuildClientVersion(1, 0, 0),
    AppID:          "", // optional
    RouteTag:       "", // optional SKRouteTag
    HTTP:           customDoer,
    Timeout:        15 * time.Second,
})
```

| Method | Notes |
|--------|-------|
| `BaseURL()` / `Token()` | Read config |
| `WithToken(token)` | Shallow-copy Client with new token |
| `BuildClientVersion(maj, min, pat)` | Pack `ClientVersion` uint32 |

### 7.2 Common HTTP headers

Authenticated bot CGI:

| Header | Value |
|--------|-------|
| `Content-Type` | `application/json` |
| `AuthorizationType` | `ilink_bot_token` |
| `Authorization` | `Bearer <token>` |
| `iLink-App-Id` | Config.AppID |
| `iLink-App-ClientVersion` | decimal ClientVersion |
| `X-WECHAT-UIN` | random base64 payload |
| `SKRouteTag` | optional |

### 7.3 CGI methods

| Method | Path constant | Notes |
|--------|---------------|-------|
| `GetUpdates(ctx, buf, longPollTimeout)` | `ilink/bot/getupdates` | Long poll; msgs + new buf |
| `SendMessage(ctx, msg)` | `ilink/bot/sendmessage` | **Empty body / `{}` treated as success** |
| `GetUploadURL(ctx, req)` | `ilink/bot/getuploadurl` | CDN upload params |
| `GetConfig(ctx, userID, token)` | `ilink/bot/getconfig` | `typing_ticket`, etc. |
| `SendTyping(ctx, userID, ticket, status)` | `ilink/bot/sendtyping` | status: 1=typing, 2=cancel; empty body OK |
| `NotifyStart` / `NotifyStop` | `ilink/bot/msg/notifystart` / `notifystop` | Lifecycle |

### 7.4 Errors

```go
type APIError struct {
    Op      string
    Ret     int
    ErrCode int
    ErrMsg   string
    Status  int // when not HTTP 200
}

ilink.IsRateLimited(err) // ret/errcode=-2 or errmsg mentions rate limit
ilink.IsStaleToken(err)  // ret/errcode=-14
```

---

## 8. Wire protocol: requests, responses, types

Paths are relative to BaseURL; bodies are JSON. Full Go types live in package `protocol`.

### 8.1 Enums

| Name | Value | Meaning |
|------|-------|---------|
| `MessageTypeUser` / `Bot` | 1 / 2 | User / bot |
| `MessageStateFinish` | 2 | Finished outbound (library default) |
| `ItemTypeText` | 1 | Text |
| `ItemTypeImage` | 2 | Image |
| `ItemTypeVoice` | 3 | Voice |
| `ItemTypeFile` | 4 | File |
| `ItemTypeVideo` | 5 | Video |
| `ItemTypeToolCallStart` | 11 | Tool start |
| `ItemTypeToolCallResult` | 12 | Tool result |
| `UploadMediaImage/Video/File/Voice` | 1/2/3/4 | getuploadurl.media_type |
| `TypingStatusTyping/Cancel` | 1 / 2 | |
| `StaleTokenErrCode` | -14 | Stale token |
| Rate-limit ret | -2 | Rate limit |

### 8.2 `getupdates`

**Request**

```json
{
  "get_updates_buf": "<cursor>",
  "base_info": { "channel_version": "1.0.0", "bot_agent": "MyBot/1.0" }
}
```

**Response**

```json
{
  "ret": 0,
  "errcode": 0,
  "errmsg": "",
  "msgs": [ /* WeixinMessage */ ],
  "get_updates_buf": "<new-cursor>",
  "longpolling_timeout_ms": 35000
}
```

### 8.3 `sendmessage`

**Request** (text sample, matches fixture)

```json
{
  "msg": {
    "to_user_id": "peer@im.wechat",
    "client_id": "weixinbot-abc",
    "message_type": 2,
    "message_state": 2,
    "context_token": "ctx-42",
    "item_list": [
      { "type": 1, "text_item": { "text": "pong" } }
    ]
  },
  "base_info": {
    "channel_version": "0.1.0",
    "bot_agent": "WeixinBot/go"
  }
}
```

**Response**

- Common success: empty body, `{}`, or `{"ret":0}` → treated as **success**
- Failure: e.g. `{"ret":-2,"errmsg":"rate limit"}` → `APIError`

### 8.4 `getuploadurl`

**Request**

```json
{
  "filekey": "<32 hex>",
  "media_type": 1,
  "to_user_id": "peer@im.wechat",
  "rawsize": 16,
  "rawfilemd5": "<md5 hex of plaintext>",
  "filesize": 32,
  "no_need_thumb": true,
  "aeskey": "<32 hex of 16-byte key>",
  "base_info": { "channel_version": "0.1.0", "bot_agent": "WeixinBot/go" }
}
```

**Response**

```json
{
  "upload_param": "...",
  "upload_full_url": "https://novac2c.cdn.weixin.qq.com/...",
  "thumb_upload_param": ""
}
```

### 8.5 `getconfig` / `sendtyping` / `notify*`

| CGI | Key request fields | Response notes |
|-----|--------------------|----------------|
| getconfig | `ilink_user_id`, `context_token` | `typing_ticket` |
| sendtyping | `ilink_user_id`, `typing_ticket`, `status` | Empty body OK |
| notifystart/stop | `base_info` | `ret` / `errmsg` |

### 8.6 `WeixinMessage` / `MessageItem` (selected fields)

**WeixinMessage**

| JSON | Notes |
|------|-------|
| `from_user_id` / `to_user_id` | Peers |
| `client_id` | Outbound id (library generates) |
| `message_type` / `message_state` | See enums |
| `item_list` | Content units |
| `context_token` | **Must echo on outbound** |
| `message_id` / `create_time_ms` | Inbound metadata |
| `session_id` / `group_id` / `run_id` | Optional |

**MessageItem**

| JSON | Notes |
|------|-------|
| `type` | ItemType* |
| `text_item` / `image_item` / `voice_item` / `file_item` / `video_item` | Payloads |
| `tool_call_start_item` / `tool_call_result_item` | Tool progress |
| `ref_msg` | Quote (flexible unmarshal; see `protocol.RefMessage`) |
| `msg_id` | Common on quote shells |

**CDNMedia**

| JSON | Notes |
|------|-------|
| `encrypt_query_param` | CDN download / reference param |
| `aes_key` | base64 form (see §9.2 encoding rules) |
| `encrypt_type` | Outbound fixed to **1** (AES) |
| `full_url` | Preferred on inbound download |

---

## 9. `media` pipeline

Session wraps upload/download; use this package for custom steps or unit tests.

### 9.1 Core APIs

| API | Notes |
|-----|-------|
| `UploadFile(ctx, client, cdn, path, to, mediaType)` | Read → getuploadurl → encrypt → CDN → `*Uploaded` |
| `DownloadItem(ctx, item, deps)` | Inbound single-item download/decrypt → `*LocalMedia` |
| `DownloadAndDecrypt` | Download when URL/key already known |
| `EncryptAES128ECB` / `DecryptAES128ECB` | 16-byte key, padded |
| `PaddedSize(n)` | Ciphertext length |
| `CDN.UploadCiphertext` / `DownloadCiphertext` | Data plane (upload retries on 5xx) |
| `NewCDN(baseURL, opts)` | CDN client |
| `SilkToWAV` / `WAVOrPCMToSilk` / `IsSilk` | Voice helpers |
| `DefaultSilkCodec` / `NilSilkCodec` / `RealSilkCodec` | Codec strategies |
| `DetectMediaKind` / `MIMEFromFilename` | Routing |
| `media.NewStore(root)` + `Save` | Inbound disk store |
| `DefaultMaxBytes` | 100 MiB |

`Uploaded`:

| Field | Notes |
|-------|-------|
| `FileKey` | hex filekey |
| `DownloadEncryptedQueryParam` | Param for sendmessage |
| `AESKeyHex` | 32-char hex |
| `FileMD5` | Plaintext MD5 hex |
| `FileSize` / `FileSizeCiphertext` | Plain / cipher sizes |

`DownloadDeps`: `CDN`, `Store`, `AccountID`, optional `SilkToWAV`.

### 9.2 AES key encoding (critical)

Aligned with openclaw-weixin / production:

| Scenario | Rule |
|----------|------|
| getuploadurl request `aeskey` | **32-char hex** (hex of 16-byte key) |
| Outbound `image` `media.aes_key` | Often `base64(raw 16 bytes)` |
| Outbound `file` / `voice` / `video` `media.aes_key` | **`base64(ASCII of the 32-char hex string)`**, not `base64(raw 16)` |
| Inbound image | Prefer `image_item.aeskey` (hex), then `media.aes_key` |
| Inbound download URL | Prefer **`full_url`**, else CDN + `encrypt_query_param` |

Wrong `aes_key` encoding often yields: upload succeeds but the phone shows a blank / unopenable file card.

### 9.3 Voice conventions

| Direction | Behavior |
|-----------|----------|
| Inbound | Decrypt SILK → try WAV; on failure keep `audio/silk`; message still delivered |
| Outbound | `.silk` pass-through; WAV/PCM encoded to Tencent SILK (`0x02#!SILK_V3`) then upload |
| Tests | Inject `media.NilSilkCodec` to force degrade paths |

### 9.4 CDN security

- Production: pin `full_url` / `upload_full_url` hosts to WeChat CDN domains (relax only in test Options).
- `SendMediaURL`: reject non-http(s), private/link-local IPs, oversized bodies (SSRF hardening).

---

## 10. `markdown`

| API | Notes |
|-----|-------|
| `Filter(s)` | Whole-string outbound filter (`SendText` default) |
| `ChunkByRunes(s, limit)` | Rune chunking (default limit 4000) |
| `StreamingFilter` | `Feed(delta)` / `Flush()` streaming filter |

If you bypass `SendText`, decide yourself whether to `Filter` and chunk.

---

## 11. `protocol` constants cheatsheet

```go
protocol.PathGetUpdates          // "ilink/bot/getupdates"
protocol.PathSendMessage         // "ilink/bot/sendmessage"
protocol.PathGetUploadURL        // "ilink/bot/getuploadurl"
protocol.PathGetConfig           // "ilink/bot/getconfig"
protocol.PathSendTyping          // "ilink/bot/sendtyping"
protocol.PathNotifyStart         // "ilink/bot/msg/notifystart"
protocol.PathNotifyStop          // "ilink/bot/msg/notifystop"

protocol.AuthorizationTypeILinkBotToken // "ilink_bot_token"

protocol.ItemTypeText / Image / Voice / File / Video
protocol.ItemTypeToolCallStart / ToolCallResult
protocol.MessageTypeBot / MessageStateFinish
protocol.UploadMediaImage / Video / File / Voice
protocol.StaleTokenErrCode // -14
```

Types: `WeixinMessage`, `MessageItem`, `RefMessage`, `CDNMedia`, `*Item`, CGI Req/Resp.  
Fixtures: `protocol/testdata/*.json`.

`RefMessage` uses flexible `UnmarshalJSON` for production `ref_msg` shells (including msg_id-only).

---

## 12. Environment variables (conventions)

The library does not read env vars itself; apps commonly use:

| Variable | Meaning |
|----------|---------|
| `ILINK_BOT_TOKEN` | Bot Bearer |
| `ILINK_ACCOUNT_ID` | Local account id (e.g. `my-bot`) |
| `ILINK_BASE_URL` | iLink origin |
| `WEIXINBOT_STATE` | State root (default `~/.weixinbot`) |

---

## 13. Error handling checklist

```go
if err := sess.SendText(ctx, to, text); err != nil {
    switch {
    case errors.Is(err, session.ErrSessionWindow):
        // Ask user to message the bot first
    case errors.Is(err, session.ErrOutboundQuota):
        // Ask user to send again to refresh window / reset count
    case ilink.IsRateLimited(err):
        // Back off, or set RetryRateLimit
    case ilink.IsStaleToken(err):
        // Re-login via QR; Run pauses ~1h
    default:
        log.Print(session.UserMessageFromError(err))
    }
}
```

| Error | Meaning |
|-------|---------|
| `session.ErrSessionWindow` | No token or window expired |
| `session.ErrOutboundQuota` | Outbound count full for this window |
| `ilink.IsRateLimited` | ret=-2 |
| `ilink.IsStaleToken` | ret/errcode=-14 |
| `*ilink.APIError` | Other CGI / HTTP failures |

---

## 14. Test injection

```go
client := ilink.NewClient(ilink.Config{
    BaseURL: "https://ilink.test",
    Token:   "t",
    HTTP:    fake.Client(),
})
sess, _ := session.New(session.Options{
    AccountID: "bot",
    Client:    client,
    Store:     store,
    HTTP:      fake.Client(), // shared CGI + CDN
    // tests only:
    // AllowAnyCDNFullURL: true,
    // MediaURLSkipDNSCheck: true,
})
```

Also see `internal/testutil` fake transport.  
Full suite: `go test ./... -count=1`.

Realistic success shapes (avoid false greens):

| CGI | Common real success |
|-----|---------------------|
| sendmessage | empty body / `{}` / `{"ret":0}` |
| sendtyping | empty body |
| getupdates | `msgs` + new `get_updates_buf`; idle may only refresh on timeout |
| getuploadurl | `upload_full_url` and/or `upload_param` |

---

## 15. Call checklist

- [ ] Credentials: token present after `SaveAccount` or `CompleteLogin`
- [ ] User must inbound first (token + window) before outbound
- [ ] Handler: `msg.Text` for commands, `msg.Body()` for models
- [ ] Media failures in `MediaErrors`; message still reaches Handler
- [ ] `msgid` quotes have no body; app may cache by MsgID
- [ ] Outbound file/voice/video `aes_key` via library path — do not hand-roll wrong base64 form
- [ ] Never enable `AllowAnyCDNFullURL` / `MediaURLSkipDNSCheck` / `auth.AllowAnyHost` in production
- [ ] Cancelable `context` for `Run`; process exit triggers notifyStop
- [ ] Multi-account via `session.Manager`, one Session per account

---

## 16. Related docs

| Doc | Content |
|-----|---------|
| [API.md](./API.md) | 中文版 / Chinese edition |
| [README.md](../README.md) | Overview and capability matrix |
| [AGENTS.md](../AGENTS.md) | Contributor / AI coding constraints |
| `go doc ./session` etc. | Package godoc |
| `protocol/testdata/` | Wire JSON fixtures |

Protocol details follow Tencent iLink / openclaw-weixin production behavior and this library’s fixtures and tests.
