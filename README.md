# weixinbot — Go iLink WeChat bot communication platform

Idiomatic Go library for **client-driven** WeChat bot messaging over the
**iLink Bot HTTP JSON** protocol.

This is **not** an OpenClaw plugin, Agent host, or webhook server. Callers own
the Agent; this module exposes `InboundMessage` events and `Send*` APIs.

**License:** MIT (see [LICENSE](./LICENSE)).

**API reference (bilingual):** [中文](./docs/API.md) · [English](./docs/API.en.md)

## Protocol model

| Direction | Mechanism |
|-----------|-----------|
| Inbound | Client long-polls `POST ilink/bot/getupdates` with `get_updates_buf` cursor |
| Outbound | Client `POST ilink/bot/sendmessage` with `to_user_id` + echoed `context_token` |
| Media | `getuploadurl` → AES-128-ECB CDN upload → `sendmessage` with CDN refs (`encrypt_type=1`) |

## Packages

| Package | Role |
|---------|------|
| `protocol` | Wire types and path constants |
| `ilink` | HTTP transport, auth headers, CGI helpers, `IsRateLimited` / `IsStaleToken` |
| `auth` | QR login (`LoginQR` / `LoginQRStdin`) plus `StartQR` / `WaitLogin` / `CompleteLogin` |
| `state` | Account credentials, sync cursor, context tokens, **peer state** (window/quota) |
| `media` | AES-ECB, CDN, upload/download, MIME routing, SILK helpers |
| `markdown` | Outbound streaming markdown filter + rune chunking |
| `session` | Run loop, policy, Send*, typing, `OpenWith`, `RunAccount` / `RunToken`, `Manager` |

## Defaults (hard policy)

| Setting | Default | Behavior |
|---------|---------|----------|
| Session window | **24h** since last inbound | `ErrSessionWindow` — **no** `sendmessage` |
| Outbound quota | **10** sends per inbound window | `ErrOutboundQuota` — **no** `sendmessage` |
| Markdown filter | **on** for `SendText` | `SendTextOptions.SkipMarkdownFilter` to disable |
| Text chunks | 4000 runes | policy checked before chunks; each chunk increments count |
| Rate-limit retry | 0 | set `RetryRateLimit` for ret=-2 only |
| CDN media | `encrypt_type=1`, `no_need_thumb=true` | locked by tests |

## Capability matrix

| Capability | API | Tests |
|------------|-----|-------|
| Protocol fixtures | `protocol` types | `protocol/types_test.go` |
| getupdates / sendmessage / getuploadurl / getconfig / sendtyping / notify* | `ilink.Client` | `ilink/client_cgi_test.go` |
| Rate limit / stale token | `ilink.IsRateLimited`, `IsStaleToken` | `ilink/errors_test.go` |
| QR login + store | `auth.LoginQR`, `LoginQRStdin`, `StartQR`, `WaitLogin`, `CompleteLogin` | `auth/login_test.go`, `auth/login_flow_test.go` |
| Peer state | `state.TouchInbound`, `IncrOutbound`, `GetPeer` | `state/peer_test.go` |
| Path sanitize | `state.SaveAccount` etc. | `state/store_test.go` |
| Inbound parse + quote | `ParseInbound`, `Quote`, `Body` (msgid shell → msg_id only) | `session/session_test.go` |
| Inbound media download | `media.DownloadItem` | `media/download_test.go` |
| SILK degrade / WAV helpers | `media.SilkToWAV`, `WAVOrPCMToSilk` | `media/download_test.go` |
| Open + bind handler | `session.Open`, `session.OpenWith` | `session/open_with_test.go` |
| Run helpers | `session.RunAccount`, `RunToken`, `LoginAndRun` | `session/run_helpers_test.go` |
| Dispatch + cursor + token | `session.Session.Run` | `session/session_test.go` |
| Policy hard block | `OutboundPolicy` via `Send*` | `session/session_test.go` |
| SendText + markdown + chunk | `SendText`, `SendTextOpts` | `session/session_test.go` |
| Send image/video/file/voice | `SendImageFile` / `SendVideoFile` / `SendFileAttachment` / `SendVoice` (aliases `SendImage` / `SendVideo` / `SendFile` / `SendVoiceFile`) | `session/session_test.go` |
| SendMedia / URL | `SendMedia`, `SendMediaURL` | `session/session_test.go` |
| Tool progress | `SendToolStart`, `SendToolResult` | `session/session_test.go` |
| SendItem | `SendItem` | `session/session_test.go` |
| Error copy | `UserMessageFromError` | `session/session_test.go` |
| Rate-limit retry | `RetryRateLimit` | `session/session_test.go` |
| Typing + config cache | `StartTyping`, `WithTyping`, `ConfigCache` | `session/session_test.go` |
| Multi-account | `session.Manager` | `session/session_test.go` |
| Quote / `ref_msg` (incl. msgid shell) | `ParseInbound`, `Quote`, `Body` | `session/session_test.go` |
| CDN upload retry (5xx) | `media.CDN.UploadCiphertext` | `media/cdn_test.go` |

## Non-goals (app / host layer)

- OpenClaw channel registration, pairing UI, slash framework, Agent runtime
- Message-history cache for quote body restore (protocol only gives `msg_id` shells; cache is caller-owned)
- Webhook servers or push inbound (client long-poll only)

## Environment variables

| Env | Meaning |
|-----|---------|
| `ILINK_BOT_TOKEN` | Bot bearer token (required unless loaded from state store) |
| `ILINK_ACCOUNT_ID` | Account id. `RunAccount` uses this when the argument is empty; `RunToken` then falls back to `default` |
| `ILINK_BASE_URL` | iLink origin (default `ilink.DefaultBaseURL`) |
| `WEIXINBOT_STATE` | State root used by examples (default `~/.weixinbot`) |

## Minimal usage

Runnable demos: [`examples/echo`](./examples/echo) (primary) and [`examples/login-qr`](./examples/login-qr).

### OpenWith (echo)

`OpenWith` binds the handler after the `*Session` exists so `Send*` / `WithTyping` need no forward-declared variable.

```go
store, err := state.NewStore(stateDir)
sess, err := session.OpenWith(store, accountID, nil, func(s *session.Session) session.Handler {
    return func(ctx context.Context, msg session.InboundMessage) error {
        return s.WithTyping(ctx, msg.FromUserID, func(ctx context.Context) error {
            return s.SendText(ctx, msg.FromUserID, "echo: "+msg.Text)
        })
    }
})
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()
_ = sess.Run(ctx)
```

`session.Open` still exists and takes a `Handler` directly.

### LoginQRStdin

```go
store, err := state.NewStore(stateDir)
res, err := auth.LoginQRStdin(ctx, store)
// res.AccountID — do not log BotToken
```

`LoginQRStdin` is `StartQR` → print `Scan QR: <QRCodeURL>` on stderr → `WaitLogin` → `CompleteLogin`.
Use `auth.LoginQR` when you need a custom `OnQR` / `VerifyCode` / HTTP fake.

### RunToken

Persist a known token (`BaseURL: ilink.DefaultBaseURL`) and block on `Run`:

```go
err := session.RunToken(ctx, stateDir, accountID, token, handler)
```

`session.RunAccount` loads an already-persisted account (argument → `ILINK_ACCOUNT_ID` → the single stored id).
`session.LoginAndRun` is `LoginQRStdin` + `Open` + `Run`.

## Advanced

### New(Options)

For a custom `ilink.Client`, outbound policy, media store, or test HTTP injection:

```go
client := ilink.NewClient(ilink.Config{
    BaseURL: ilink.DefaultBaseURL,
    Token:   token,
    HTTP:    httpDoer, // tests: FakeTransport
})
sess, err := session.New(session.Options{
    AccountID: accountID,
    Client:    client,
    Store:     store,
    HTTP:      httpDoer,
    Handler:   handler,
})
_ = sess.Run(ctx)
```

Lower-level QR (same wire behavior as `LoginQR`): `auth.StartQR`, `auth.WaitLogin`, `auth.CompleteLogin`.

Inject `http.RoundTripper` via `ilink.Config.HTTP` / `session.Options.HTTP` for tests.

## SILK / voice notes

- **Default codec:** pure-Go SKP Silk via `github.com/wdvxdr1123/go-silk` (no CGO) — `media.RealSilkCodec` is `media.DefaultSilkCodec`.
- **Inbound:** decrypt SILK from CDN → decode to WAV (`audio/wav`); on transcode failure keep raw SILK (`audio/silk`). Message is still delivered.
- **Outbound:** `.silk` pass-through; WAV/PCM encoded to Tencent SILK (`0x02#!SILK_V3`) before upload. Inject `media.NilSilkCodec` only to force degrade paths in tests.
- Pin: see `go.mod` for the locked go-silk version.

## Errors

| Error | Meaning |
|-------|---------|
| `session.ErrSessionWindow` | No `context_token` or last inbound older than window |
| `session.ErrOutboundQuota` | ≥ N outbound sends since last inbound |
| `ilink.IsRateLimited` | ret=-2 / rate limit errmsg |
| `ilink.IsStaleToken` | ret/errcode -14 (Run pauses ~1h) |

`session.UserMessageFromError` maps these to short Chinese strings for Agent UX.

## Test / build

```bash
go test ./... -count=1
go test -race ./...   # when race detector available
go build ./examples/echo ./examples/login-qr
```
