# weixinbot 接口调用文档

> **语言 / Language:** [中文](./API.md) | [English](./API.en.md)

Module：`github.com/fun7257/weixinbot`  
定位：**微信 iLink Bot 通讯库**（非 Agent / OpenClaw / Webhook）。  
Go：`go 1.22+`（以 `go.mod` 为准）。

```text
微信用户 ──iLink HTTP + CDN──► weixinbot ──► 你的 Agent / Handler
```

| 方向 | 机制 |
|------|------|
| 入站 | 客户端长轮询 `POST ilink/bot/getupdates` + `get_updates_buf` 游标 |
| 出站 | 客户端 `POST ilink/bot/sendmessage`，回传 `context_token` |
| 媒体 | `getuploadurl` → AES-128-ECB CDN 上传 → `sendmessage` 带 CDN 引用 |

| 默认 | 值 |
|------|-----|
| Base | `https://ilinkai.weixin.qq.com` |
| CDN | `https://novac2c.cdn.weixin.qq.com/c2c` |
| 会话窗口 | **24h**（距上次用户入站） |
| 出站配额 | **10** 条 / 窗口 |

---

## 目录

1. [安装与依赖](#1-安装与依赖)
2. [推荐调用路径](#2-推荐调用路径90-场景)
3. [包一览与依赖方向](#3-包一览与依赖方向)
4. [`session` — 主 API](#4-session--主-api)
5. [`auth` — 扫码登录](#5-auth--扫码登录)
6. [`state` — 持久化](#6-state--持久化)
7. [`ilink` — 底层 CGI](#7-ilink--底层-cgi)
8. [线协议：请求 / 响应 / 类型](#8-线协议请求--响应--类型)
9. [`media` — 媒体流水线](#9-media--媒体流水线)
10. [`markdown`](#10-markdown)
11. [`protocol` 常量速查](#11-protocol-常量速查)
12. [环境变量约定](#12-环境变量约定)
13. [错误处理清单](#13-错误处理清单)
14. [测试注入](#14-测试注入)
15. [调用检查表](#15-调用检查表)
16. [相关文档](#16-相关文档)

---

## 1. 安装与依赖

```bash
go get github.com/fun7257/weixinbot@latest
```

本地开发（example / monorepo）：

```go
// go.mod
replace github.com/fun7257/weixinbot => ../weixinbot
```

```go
import (
    "github.com/fun7257/weixinbot/auth"
    "github.com/fun7257/weixinbot/ilink"
    "github.com/fun7257/weixinbot/session"
    "github.com/fun7257/weixinbot/state"
    // 按需: media, markdown, protocol
)
```

语音编解码默认依赖：`github.com/wdvxdr1123/go-silk`（纯 Go，无 CGO）。

---

## 2. 推荐调用路径（90% 场景）

```text
state.NewStore → 登录或 SaveAccount
       ↓
session.New / session.Open
       ↓
Handler(InboundMessage) 内调用 sess.Send*
       ↓
sess.Run(ctx)   // 阻塞直到 cancel；退出时 best-effort notifyStop
```

### 2.1 最小可运行示例

```go
package main

import (
    "context"
    "log"
    "os"
    "os/signal"
    "path/filepath"
    "syscall"

    "github.com/fun7257/weixinbot/ilink"
    "github.com/fun7257/weixinbot/session"
    "github.com/fun7257/weixinbot/state"
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
        Token:   os.Getenv("ILINK_BOT_TOKEN"), // 或 auth 扫码后落盘
        BaseURL: ilink.DefaultBaseURL,
    })

    var sess *session.Session
    sess, err = session.Open(store, accountID, nil, func(ctx context.Context, msg session.InboundMessage) error {
        // 命令路由用 msg.Text；给模型展示用 msg.Body()（含引用摘要）
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

### 2.2 扫码登录后运行

```go
start, err := auth.StartQR(ctx, "cli", auth.Options{
    VerifyCode: auth.StdinVerifyCode(os.Stdin, os.Stderr), // 需要验证码时
})
// 展示 start.QRCodeURL 给用户扫码
res, err := auth.WaitLogin(ctx, start.SessionKey, auth.Options{/* 同上 */})
_ = auth.CompleteLogin(store, res)
// 再 Open + Run；accountID 可用 res.AccountID
```

### 2.3 端到端时序（文本）

```text
[App]  Run(ctx)
  │
  ├─► NotifyStart
  │
  └─► loop:
        GetUpdates(buf)  ──long poll──►  iLink
              │
              ├─ msgs[] → TouchInbound(token) → 下载媒体 → Handler
              │                │
              │                └─ Handler 内 SendText / SendImage…
              │                       │
              │                       ├─ TryReserveOutbound (窗口/配额)
              │                       └─ SendMessage(msg + context_token)
              │
              └─ SaveSyncBuf(新 buf)
  cancel → NotifyStop (best effort)
```

### 2.4 端到端时序（出站媒体）

```text
SendImageFile / SendFileAttachment / …
  │
  ├─ Policy.Check + TryReserveOutbound
  ├─ 可选：caption → SendText 分片
  ├─ media.UploadFile:
  │     读文件 → 随机 aeskey/filekey
  │     GetUploadURL(rawsize, md5, aeskey hex, no_need_thumb)
  │     AES-128-ECB 加密 → CDN PUT/POST
  └─ SendMessage(item + media.encrypt_query_param + media.aes_key)
```

---

## 3. 包一览与依赖方向

| 包 | 职责 | 典型调用方 |
|----|------|------------|
| `session` | Run 循环、入站解析、Send*、策略、typing、多账号 | **应用主入口** |
| `auth` | 扫码登录状态机 | 首次绑定 / 换号 |
| `state` | 凭证、游标、context_token、peer 配额 | 持久化 |
| `ilink` | HTTP CGI 与错误分类 | 高级 / 自建循环 |
| `media` | AES/CDN/SILK/MIME | 高级媒体流水线 |
| `markdown` | 出站文本清洗、按 rune 分片 | 自定义发送前处理 |
| `protocol` | 线类型与路径常量 | 扩展/调试 |

依赖方向（**禁止**反向 import）：

```text
protocol ← ilink, media, session, auth
state    ← auth, session
ilink    ← auth, media, session
media    ← session
markdown ← session
```

---

## 4. `session` — 主 API

### 4.1 构造

| API | 签名要点 | 说明 |
|-----|----------|------|
| `session.New` | `New(opts Options) (*Session, error)` | 完整配置；**不**启动轮询 |
| `session.Open` | `Open(store, accountID, httpDoer, handler)` | 从 store 读账号并 `New`；`httpDoer` 可为 nil |
| `(*Session).Run` | `Run(ctx) error` | 长轮询直到 `ctx` 取消；内部 `NotifyStart` / `NotifyStop` |

`Open` 等价于：`LoadAccount` → `ilink.NewClient` → `New(Options{...})`。

### 4.2 `Options` 全字段

| 字段 | 类型 | 默认 | 说明 |
|------|------|------|------|
| `AccountID` | string | **必填** | 本地账号 id（路径消毒） |
| `Client` | `*ilink.Client` | **必填** | iLink 控制面 |
| `Store` | `*state.Store` | **必填** | 凭证 / peer / 游标 |
| `CDNBaseURL` | string | `ilink.DefaultCDNBaseURL` | 媒体数据面 |
| `HTTP` | `ilink.Doer` | nil | 注入 Fake（CGI + CDN 共用） |
| `Handler` | `Handler` | nil | 每条入站**顺序**回调 |
| `Logger` | `*slog.Logger` | `slog.Default()` | |
| `LongPollTimeout` | `time.Duration` | 35s | getupdates 客户端 hold |
| `RetryDelay` | `time.Duration` | 2s | 轮询瞬时错误退避 |
| `StaleTokenPause` | `time.Duration` | 1h | ret/errcode=-14 暂停 |
| `Policy` | `OutboundPolicy` | 24h / 10 | 出站硬策略 |
| `TextChunkLimit` | int | 4000 | 文本按 **rune** 分片 |
| `RetryRateLimit` | int | 0 | sendmessage ret=-2 重试次数 |
| `RateLimitBackoff` | `time.Duration` | 200ms | 限流重试初始退避 |
| `MediaStore` | `*media.Store` | Store 下临时目录 | 入站媒体落盘 |
| `SilkCodec` | `media.SilkCodec` | `DefaultSilkCodec` | 语音编解码 |
| `OnHandlerError` | func | slog | Handler 返回错误时 |
| `OnMediaError` | func | slog | 入站媒体下载失败（**消息仍交付**） |
| `MaxMediaDownloadBytes` | int64 | 100MiB | 入站下载上限 |
| `MaxMediaURLBytes` | int64 | 50MiB | `SendMediaURL` 上限 |
| `AllowAnyCDNFullURL` | bool | **false** | 仅测试：放宽 CDN host 钉死 |
| `MediaURLSkipDNSCheck` | bool | **false** | 仅测试：跳过 SSRF DNS 校验 |
| `TypingKeepalive` | `time.Duration` | 5s | `WithTyping` 周期 |
| `Now` | `func() time.Time` | `time.Now` | 测试时钟 |

```go
type Handler func(ctx context.Context, msg InboundMessage) error
```

### 4.3 入站：`InboundMessage`

| 字段 / 方法 | 类型 | 用途 |
|-------------|------|------|
| `AccountID` | string | 本 Session 账号 |
| `FromUserID` | string | 发送方；出站 `toUserID` |
| `ContextToken` | string | 会话粘合（库已自动存/回传） |
| `Text` | string | **当前消息正文**（无引用前缀，适合 `/command`） |
| `Body()` | string | 给模型/日志：`[引用: …]\n` + Text |
| `Quote` | `*Quote` | 结构化引用，见 §4.4 |
| `Media` | `[]media.LocalMedia` | 已下载解密的本地文件 |
| `MediaErrors` | `[]string` | 单条媒体失败原因（消息仍会回调） |
| `Items` | `[]protocol.MessageItem` | 原始 item_list |
| `Raw` | `protocol.WeixinMessage` | 完整线消息 |
| `MessageID` | int64 | 元数据 |
| `CreateTimeMs` | int64 | 元数据 |
| `ReceivedAt` | `time.Time` | 本地接收时间 |

`media.LocalMedia`：

| 字段 | 说明 |
|------|------|
| `Kind` | `image` \| `voice` \| `file` \| `video` |
| `Path` | 本地绝对路径 |
| `MIME` | 如 `image/png`、`audio/wav`、`audio/silk` |
| `FileName` | 入站文件名（file 等） |

```go
func handler(ctx context.Context, msg session.InboundMessage) error {
    if strings.HasPrefix(strings.TrimSpace(msg.Text), "/") {
        // 命令路由 — 必须用 Text，不要用 Body()
    }
    _ = msg.Body() // 展示给 LLM
    for _, m := range msg.Media {
        // m.Kind / m.Path / m.MIME
    }
    if len(msg.MediaErrors) > 0 {
        // 部分媒体失败，文本仍可用
    }
    return sess.SendText(ctx, msg.FromUserID, "ok")
}
```

无网络解析（测试 / 自定义轮询）：

```go
parsed := session.ParseInbound(raw) // Text / Quote / Body，不下载媒体
```

调试：`session.FormatInboundDebug(msg)`。

### 4.4 引用 `Quote`

| Kind 常量 | 值 | 含义 | `Body()` 行为 |
|-----------|-----|------|---------------|
| `QuoteKindText` | `text` | title / text_item | `[引用: title \| text]\n…` |
| `QuoteKindImage` 等 | `image`/`voice`/`file`/`video` | 媒体引用 | 仅当前 Text（媒体在 Quote） |
| `QuoteKindMsgID` | `msgid` | **真机常见**：`type=0` + 仅 `msg_id` | `[引用: msg_id=…]\n…` |
| `QuoteKindUnknown` | `unknown` | 非空但未识别 | 带 RawJSON 诊断 |

`Quote` 字段：

| 字段 | 说明 |
|------|------|
| `Title` | 服务端摘要（发送者名 / 短预览） |
| `Text` | 嵌入正文；msgid 壳时为 MsgID 本身（**非**被引用原文） |
| `Kind` | 上表 |
| `MsgID` | `ref_msg` 内 `msg_id` |
| `Media` | 媒体引用标记（默认无 Path） |
| `Raw` | 线 `message_item` |
| `RawJSON` | 原始 `ref_msg` JSON |

辅助：`HasContent()` / `IsMedia()` / `IsMsgIDShell()` / `Summary()`。

**重要：**

- 协议在 msgid 壳场景**不**下发被引用正文。
- 库**不**做消息历史缓存。
- 若需「还原引用原文」，应用按 `Quote.MsgID` 自行缓存入站（例如内存 LRU）。

```go
if msg.Quote != nil {
    switch msg.Quote.Kind {
    case session.QuoteKindMsgID:
        // msg.Quote.MsgID — 应用层查本地历史
    case session.QuoteKindImage:
        // 引用了图片
    }
}
```

### 4.5 出站 Send*

所有 Send* 在调用 `sendmessage` **之前**做策略检查（缺 token / 超 24h / 超配额则直接返回错误，**不打网**）。  
策略通过后 `TryReserveOutbound`；发送失败会 `ReleaseOutbound` 回滚预留。

| API | 说明 |
|-----|------|
| `SendText(ctx, to, text)` | Markdown 默认清洗 + 4000 rune 分片；**每片计 1 次配额** |
| `SendTextOpts(ctx, to, text, opts)` | `SendTextOptions{SkipMarkdownFilter: true}` |
| `SendItem(ctx, to, item)` | 原始 `protocol.MessageItem` |
| `SendToolStart(ctx, to, name, callID)` | type=11 工具开始 |
| `SendToolResult(ctx, to, name, callID, status)` | type=12 工具结果 |
| `SendImageFile(ctx, to, path, caption)` | 上传 + 发图；caption 可先发文本分片 |
| `SendVideoFile(ctx, to, path, caption)` | 视频 |
| `SendFileAttachment(ctx, to, path)` | 文件附件 |
| `SendVoice(ctx, to, path)` | `.silk` 直传；WAV/PCM → SILK 再传 |
| `SendMedia(ctx, to, path, caption)` | 按扩展名路由到上列 |
| `SendMediaURL(ctx, to, url, caption)` | 拉公网 URL 再发（SSRF 校验：禁私网 IP 等） |

```go
_ = sess.SendText(ctx, to, "hello")
_ = sess.SendTextOpts(ctx, to, "**raw**", session.SendTextOptions{SkipMarkdownFilter: true})

_ = sess.SendImageFile(ctx, to, "/path/a.png", "说明")
_ = sess.SendFileAttachment(ctx, to, "/path/report.pdf")
_ = sess.SendVoice(ctx, to, "/path/a.wav")
_ = sess.SendMedia(ctx, to, "/path/clip.mp4", "")
_ = sess.SendMediaURL(ctx, to, "https://example.com/a.png", "")

_ = sess.SendToolStart(ctx, to, "search", "call-1")
_ = sess.SendToolResult(ctx, to, "search", "call-1", "completed")
```

辅助：

| API | 说明 |
|-----|------|
| `StartTyping` / `StopTyping` | 输入状态（内部 getconfig 缓存 `typing_ticket`） |
| `WithTyping(ctx, userID, fn)` | fn 期间周期 keepalive typing |
| `Peer(userID)` | 读 `state.PeerState` |
| `ContextToken(userID)` | 当前 token |
| `AccountID` / `Client` / `Store` | 访问底层 |

`ConfigCache`（session 内部）：按用户缓存 `getconfig` 的 `typing_ticket`，TTL ~24h + 抖动，失败指数退避（上限 1h）。应用一般不直接使用。

### 4.6 出站策略

| 条件 | 错误 | `UserMessageFromError` 示意 |
|------|------|------------------------------|
| 无 `context_token`，或距上次用户入站 > 窗口 | `session.ErrSessionWindow` | 会话已过期… |
| 本窗口出站条数 ≥ 配额 | `session.ErrOutboundQuota` | 本轮回复条数已达上限… |
| ret=-2 rate limited | `ilink.IsRateLimited` | 发送过于频繁… |
| ret/errcode=-14 | `ilink.IsStaleToken` | 登录已失效… |

```go
type OutboundPolicy struct {
    SessionWindow time.Duration // 0 → DefaultSessionWindow (24h)
    OutboundQuota int           // 0 → DefaultOutboundQuota (10)
}
```

默认硬策略**不可**在生产随意关掉（可改数值；配额/窗口为 0 时会套用默认，而非禁用）。

```go
sess, _ = session.New(session.Options{
    // ...
    Policy: session.OutboundPolicy{
        SessionWindow: 24 * time.Hour,
        OutboundQuota: 10,
    },
    RetryRateLimit: 1, // 可选：限流重试 1 次
})
```

用户再发一条入站 → `TouchInbound` → **出站计数清零**、窗口刷新。

### 4.7 多账号 `Manager`

```go
mgr := session.NewManager()
_ = mgr.Start(ctx, sessA)
_ = mgr.Start(ctx, sessB)
_ = mgr.Stop("account-a")
mgr.StopAll()
list := mgr.List()
s, ok := mgr.Get("account-a")
```

每个账号独立 `Session` + Store 内凭证；出站由调用方选 Session（无隐式全局 bot）。

---

## 5. `auth` — 扫码登录

| API | 说明 |
|-----|------|
| `StartQR(ctx, sessionKey, opts)` | 取二维码；返回 `QRStart{QRCodeURL, SessionKey, RawQRCode}` |
| `WaitLogin(ctx, sessionKey, opts)` | 轮询扫码状态至成功/失败/超时 |
| `CompleteLogin(store, result)` | 写入 token / baseURL / userID；`RegisterAccountID` |
| `StdinVerifyCode(r, w)` | 终端注入验证码 |

### 5.1 `Options`

| 字段 | 默认 | 说明 |
|------|------|------|
| `BaseURL` | `FixedLoginBaseURL`（同 ilink 默认域） | 登录 API origin |
| `BotType` | `"3"` | |
| `HTTP` | 默认 client | 可注入 Fake |
| `LocalTokens` | nil | `local_token_list` |
| `VerifyCode` | nil | need_verifycode 流程必填 |
| `MaxQRRefresh` | 3 | 二维码刷新次数上限 |
| `PollInterval` | 约 1s 量级 | 状态轮询间隔 |
| `StatusTimeout` | 35s | 单次 status 请求超时 |
| `AllowAnyHost` | false | **仅测试** |
| `AllowedHostSuffixes` | weixin.qq.com / qq.com | redirect 白名单 |

### 5.2 `LoginResult`

| 字段 | 说明 |
|------|------|
| `Connected` / `AlreadyConnected` | 是否新连上 / 已连接 |
| `BotToken` | Bearer token |
| `AccountID` | 建议作 store 账号 id（ilink_bot_id） |
| `BaseURL` | 业务 base |
| `UserID` | 微信侧 user id |
| `Message` | 附加说明 |

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

测试：`ResetActiveLoginsForTest()`；生产勿开 `AllowAnyHost`。

---

## 6. `state` — 持久化

### 6.1 目录布局

```text
$STATE_ROOT/
  accounts.json                         # 账号 id 索引
  accounts/{id}.json                    # Account
  accounts/{id}.sync.json               # get_updates_buf
  accounts/{id}.context-tokens.json     # userID → token（实现细节）
  accounts/{id}.peers.json              # peer 状态（实现细节）
```

### 6.2 `Account`

| 字段 | JSON | 说明 |
|------|------|------|
| `Token` | `token` | Bot Bearer |
| `BaseURL` | `baseUrl` | iLink origin |
| `UserID` | `userId` | 可选 |
| `CDNBaseURL` | `cdnBaseUrl` | 可选，覆盖默认 CDN |

### 6.3 API

| API | 说明 |
|-----|------|
| `NewStore(root)` | 创建根目录 |
| `RegisterAccountID` / `ListAccountIDs` | 账号索引 |
| `SaveAccount` / `LoadAccount` | 凭证 |
| `DeleteAccount` | 删除账号相关文件 |
| `LoadSyncBuf` / `SaveSyncBuf` | 长轮询游标 |
| `SetContextToken` / `GetContextToken` / `RestoreContextTokens` | 会话 token |
| `ResolveContextToken` | 解析当前 token |
| `TouchInbound` | 入站：更新 token、时间、**清零**出站计数 |
| `GetPeer` | `PeerState{ContextToken, LastInboundAt, OutboundCount}` |
| `IncrOutbound` | 简单 +1（Send 路径优先用 Reserve） |
| `TryReserveOutbound` | 原子预留出站配额（Send 内部） |
| `ReleaseOutbound` | 失败回滚预留 |
| `ClearStaleAccountsForUserID` | 同 userId 清旧号 |

`accountID` 会做路径消毒（禁止 `..`、`/` 等非法分量）。

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

## 7. `ilink` — 底层 CGI

一般使用 `session` 即可。自建循环或调试时直接调 Client。

### 7.1 构造

```go
client := ilink.NewClient(ilink.Config{
    BaseURL:        ilink.DefaultBaseURL,
    Token:          token,
    ChannelVersion: "1.0.0",
    BotAgent:       "MyBot/1.0",
    ClientVersion:  ilink.BuildClientVersion(1, 0, 0),
    AppID:          "", // 可选
    RouteTag:       "", // 可选 SKRouteTag
    HTTP:           customDoer,
    Timeout:        15 * time.Second,
})
```

| 方法 | 说明 |
|------|------|
| `BaseURL()` / `Token()` | 读配置 |
| `WithToken(token)` | 返回换 token 的浅拷贝 Client |
| `BuildClientVersion(maj, min, pat)` | 打包 `ClientVersion` uint32 |

### 7.2 HTTP 公共头

带鉴权的 bot CGI：

| Header | 值 |
|--------|-----|
| `Content-Type` | `application/json` |
| `AuthorizationType` | `ilink_bot_token` |
| `Authorization` | `Bearer <token>` |
| `iLink-App-Id` | Config.AppID |
| `iLink-App-ClientVersion` | 十进制 ClientVersion |
| `X-WECHAT-UIN` | 随机 base64 载荷 |
| `SKRouteTag` | 可选 |

### 7.3 CGI 方法

| 方法 | 路径常量 | 说明 |
|------|----------|------|
| `GetUpdates(ctx, buf, longPollTimeout)` | `ilink/bot/getupdates` | 长轮询；返回 msgs + 新 buf |
| `SendMessage(ctx, msg)` | `ilink/bot/sendmessage` | **空 body / `{}` 视为成功** |
| `GetUploadURL(ctx, req)` | `ilink/bot/getuploadurl` | 取 CDN 上传参数 |
| `GetConfig(ctx, userID, token)` | `ilink/bot/getconfig` | 取 `typing_ticket` 等 |
| `SendTyping(ctx, userID, ticket, status)` | `ilink/bot/sendtyping` | status: 1=typing, 2=cancel；空 body 可成功 |
| `NotifyStart` / `NotifyStop` | `ilink/bot/msg/notifystart` / `notifystop` | 会话生命周期 |

### 7.4 错误类型

```go
type APIError struct {
    Op      string
    Ret     int
    ErrCode int
    ErrMsg   string
    Status  int // 非 200 时
}

ilink.IsRateLimited(err) // ret/errcode=-2 或 errmsg 含 rate limit
ilink.IsStaleToken(err)  // ret/errcode=-14
```

---

## 8. 线协议：请求 / 响应 / 类型

路径均相对 BaseURL；body 为 JSON。完整 Go 类型见 `protocol` 包。

### 8.1 枚举

| 名称 | 值 | 含义 |
|------|-----|------|
| `MessageTypeUser` / `Bot` | 1 / 2 | 用户 / 机器人 |
| `MessageStateFinish` | 2 | 出站完成态（库默认） |
| `ItemTypeText` | 1 | 文本 |
| `ItemTypeImage` | 2 | 图 |
| `ItemTypeVoice` | 3 | 语音 |
| `ItemTypeFile` | 4 | 文件 |
| `ItemTypeVideo` | 5 | 视频 |
| `ItemTypeToolCallStart` | 11 | 工具开始 |
| `ItemTypeToolCallResult` | 12 | 工具结果 |
| `UploadMediaImage/Video/File/Voice` | 1/2/3/4 | getuploadurl.media_type |
| `TypingStatusTyping/Cancel` | 1 / 2 | |
| `StaleTokenErrCode` | -14 | token 失效 |
| 限流 ret | -2 | rate limit |

### 8.2 `getupdates`

**请求**

```json
{
  "get_updates_buf": "<cursor>",
  "base_info": { "channel_version": "1.0.0", "bot_agent": "MyBot/1.0" }
}
```

**响应**

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

**请求**（文本示例，与 fixture 一致）

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

**响应**

- 常见：空 body、或 `{}`、或 `{"ret":0}` → 库视为**成功**
- 失败：`{"ret":-2,"errmsg":"rate limit"}` 等 → `APIError`

### 8.4 `getuploadurl`

**请求**

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

**响应**

```json
{
  "upload_param": "...",
  "upload_full_url": "https://novac2c.cdn.weixin.qq.com/...",
  "thumb_upload_param": ""
}
```

### 8.5 `getconfig` / `sendtyping` / `notify*`

| CGI | 关键字段 | 响应要点 |
|-----|----------|----------|
| getconfig | `ilink_user_id`, `context_token` | `typing_ticket` |
| sendtyping | `ilink_user_id`, `typing_ticket`, `status` | 空 body 可成功 |
| notifystart/stop | `base_info` | `ret` / `errmsg` |

### 8.6 `WeixinMessage` / `MessageItem` 字段（节选）

**WeixinMessage**

| JSON | 说明 |
|------|------|
| `from_user_id` / `to_user_id` | 对端 |
| `client_id` | 出站幂等/追踪（库自动生成） |
| `message_type` / `message_state` | 见枚举 |
| `item_list` | 内容单元 |
| `context_token` | **出站必须回传** |
| `message_id` / `create_time_ms` | 入站元数据 |
| `session_id` / `group_id` / `run_id` | 可选 |

**MessageItem**

| JSON | 说明 |
|------|------|
| `type` | ItemType* |
| `text_item` / `image_item` / `voice_item` / `file_item` / `video_item` | 载荷 |
| `tool_call_start_item` / `tool_call_result_item` | 工具进度 |
| `ref_msg` | 引用（灵活反序列化，见 `protocol.RefMessage`） |
| `msg_id` | 引用壳常见 |

**CDNMedia（媒体引用）**

| JSON | 说明 |
|------|------|
| `encrypt_query_param` | CDN 下载/引用参数 |
| `aes_key` | base64 形态（见 §9.2 编码规则） |
| `encrypt_type` | 出站固定 **1**（AES） |
| `full_url` | 入站优先用完整 URL（库 download 优先） |

---

## 9. `media` — 媒体流水线

Session 已封装上下传；需要拆步骤或单测时用本包。

### 9.1 核心 API

| API | 说明 |
|-----|------|
| `UploadFile(ctx, client, cdn, path, to, mediaType)` | 读文件 → getuploadurl → 加密 → CDN → `*Uploaded` |
| `DownloadItem(ctx, item, deps)` | 入站单 item 下载解密 → `*LocalMedia` |
| `DownloadAndDecrypt` | 已知 URL/key 时下载解密 |
| `EncryptAES128ECB` / `DecryptAES128ECB` | 16 字节 key，PKCS 风格填充 |
| `PaddedSize(n)` | 密文长度 |
| `CDN.UploadCiphertext` / `DownloadCiphertext` | 数据面（上传对 5xx 有重试） |
| `NewCDN(baseURL, opts)` | 构造 CDN 客户端 |
| `SilkToWAV` / `WAVOrPCMToSilk` / `IsSilk` | 语音 |
| `DefaultSilkCodec` / `NilSilkCodec` / `RealSilkCodec` | 编解码策略 |
| `DetectMediaKind` / `MIMEFromFilename` | 路由 |
| `media.NewStore(root)` + `Save` | 入站落盘 |
| `DefaultMaxBytes` | 100 MiB |

`Uploaded`：

| 字段 | 说明 |
|------|------|
| `FileKey` | hex filekey |
| `DownloadEncryptedQueryParam` | 回传给 sendmessage 的 param |
| `AESKeyHex` | 32 字符 hex |
| `FileMD5` | 明文 MD5 hex |
| `FileSize` / `FileSizeCiphertext` | 明文 / 密文长度 |

`DownloadDeps`：`CDN`、`Store`、`AccountID`、`SilkToWAV`（可选）。

### 9.2 AES Key 编码（关键）

与 openclaw-weixin / 真机行为对齐：

| 场景 | 规则 |
|------|------|
| getuploadurl 请求 `aeskey` | **32 字符 hex**（16 字节 key 的 hex） |
| 出站 `image` 的 `media.aes_key` | 常见：`base64(原始 16 字节)` |
| 出站 `file` / `voice` / `video` 的 `media.aes_key` | **`base64(ASCII 的 32 位 hex 字符串)`**，不是 `base64(raw 16)` |
| 入站 image | 优先 `image_item.aeskey`（hex）→ 再 `media.aes_key` |
| 入站下载 URL | **优先 `full_url`**，再拼 CDN + `encrypt_query_param` |

错误的 `aes_key` 编码会导致：上传成功但手机侧文件卡片空白 / 打不开。

### 9.3 语音约定

| 方向 | 行为 |
|------|------|
| 入站 | 解密 SILK → 尽量转 WAV；失败则保留 `audio/silk`，消息仍交付 |
| 出站 | `.silk` 直传；WAV/PCM 编码为腾讯 SILK（`0x02#!SILK_V3`）再上传 |
| 测试 | 注入 `media.NilSilkCodec` 强制降级路径 |

### 9.4 CDN 安全

- 生产：`full_url` / `upload_full_url` host 钉死在微信 CDN 域（可用 Options 测试放宽）。
- `SendMediaURL`：拒绝非 http(s)、私网/链路本地 IP、过大 body 等（SSRF 硬化）。

---

## 10. `markdown`

| API | 说明 |
|-----|------|
| `Filter(s)` | 出站整段清洗（`SendText` 默认调用） |
| `ChunkByRunes(s, limit)` | 按 rune 分片（默认 limit=4000） |
| `StreamingFilter` | `Feed(delta)` / `Flush()` 流式清洗 |

自定义发送路径若绕过 `SendText`，应自行决定是否 `Filter` + 分片。

---

## 11. `protocol` 常量速查

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

类型：`WeixinMessage`、`MessageItem`、`RefMessage`、`CDNMedia`、各 `*Item`、各 CGI Req/Resp。  
fixture：`protocol/testdata/*.json`。

`RefMessage` 使用灵活 `UnmarshalJSON`，兼容生产多种 `ref_msg` 壳（含仅 `msg_id`）。

---

## 12. 环境变量约定

库本身不读 env；应用层约定（example / 部署常用）：

| 变量 | 含义 |
|------|------|
| `ILINK_BOT_TOKEN` | Bot Bearer |
| `ILINK_ACCOUNT_ID` | 本地账号 id（默认如 `my-bot`） |
| `ILINK_BASE_URL` | iLink origin |
| `WEIXINBOT_STATE` | 状态目录（默认 `~/.weixinbot`） |

---

## 13. 错误处理清单

```go
if err := sess.SendText(ctx, to, text); err != nil {
    switch {
    case errors.Is(err, session.ErrSessionWindow):
        // 提示用户先给 bot 发一条消息
    case errors.Is(err, session.ErrOutboundQuota):
        // 提示再发一条以刷新窗口 / 重置计数
    case ilink.IsRateLimited(err):
        // 退避后重试，或设 RetryRateLimit
    case ilink.IsStaleToken(err):
        // 重新扫码登录；Run 内会暂停约 1h
    default:
        log.Print(session.UserMessageFromError(err))
    }
}
```

| 错误 | 含义 |
|------|------|
| `session.ErrSessionWindow` | 无 token 或窗口过期 |
| `session.ErrOutboundQuota` | 本窗口出站条数已满 |
| `ilink.IsRateLimited` | ret=-2 |
| `ilink.IsStaleToken` | ret/errcode=-14 |
| `*ilink.APIError` | 其它 CGI / HTTP 失败 |

---

## 14. 测试注入

```go
// Fake RoundTripper / 自定义 Doer
client := ilink.NewClient(ilink.Config{
    BaseURL: "https://ilink.test",
    Token:   "t",
    HTTP:    fake.Client(),
})
sess, _ := session.New(session.Options{
    AccountID: "bot",
    Client:    client,
    Store:     store,
    HTTP:      fake.Client(), // CGI + CDN 共用
    // 仅测试:
    // AllowAnyCDNFullURL: true,
    // MediaURLSkipDNSCheck: true,
})
```

包内还提供 `internal/testutil` Fake transport。  
完整回归：`go test ./... -count=1`。

可信响应形态（避免假绿）：

| CGI | 真机常见成功形态 |
|-----|------------------|
| sendmessage | 空 body / `{}` / `{"ret":0}` |
| sendtyping | 空 body |
| getupdates | 含 `msgs` + 新 `get_updates_buf`；空闲时仅超时刷新 |
| getuploadurl | `upload_full_url` 和/或 `upload_param` |

---

## 15. 调用检查表

- [ ] 凭证：`SaveAccount` 或 `CompleteLogin` 后有 token  
- [ ] 必须先有用户入站（token + 窗口）再出站  
- [ ] Handler 用 `msg.Text` 做命令，用 `msg.Body()` 给模型  
- [ ] 媒体失败看 `MediaErrors`，消息仍会进 Handler  
- [ ] 引用 `msgid` 不保证有原文，应用可按 MsgID 自建缓存  
- [ ] 出站 file/voice/video 的 `aes_key` 编码走库内路径，勿手写错 base64 形态  
- [ ] 生产勿开 `AllowAnyCDNFullURL` / `MediaURLSkipDNSCheck` / `auth.AllowAnyHost`  
- [ ] `Run` 用可取消的 `context`，进程退出会 notifyStop  
- [ ] 多账号用 `session.Manager`，每账号独立 Session  

---

## 16. 相关文档

| 文档 | 内容 |
|------|------|
| [API.en.md](./API.en.md) | English edition / 英文版 |
| [README.md](../README.md) | 总览与能力矩阵 |
| [AGENTS.md](../AGENTS.md) | 贡献 / AI 编码硬约束 |
| `go doc ./session` 等 | 包级 godoc |
| `protocol/testdata/` | 线协议 JSON fixture |

协议细节以腾讯 iLink / openclaw-weixin 生产行为与本库 fixture、集成测试为准。
