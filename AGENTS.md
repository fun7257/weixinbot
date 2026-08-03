# AGENTS.md — weixinbot

Hard rules for humans and coding agents. Prefer short, enforceable directives over essays.

## Goals

- This is a **Go library** for client-driven WeChat bot messaging over the **iLink Bot HTTP JSON** protocol.
- Changes must be **minimal, reviewable, and idiomatic Go**.
- Do **not** break wire behavior, policy semantics, or exported APIs unless the task explicitly requires it.
- Prefer **root-cause fixes**; no drive-by refactors or unrelated cleanup.
- Code, comments, and commits are in **English**. Do **not** add AI attribution in commits or comments.

## Project Snapshot

- Module: `github.com/fun7257/weixinbot` (see `go.mod`).
- **Not** an OpenClaw plugin, Agent host, or webhook server.
- Callers own the Agent. This module exposes `InboundMessage` events and `Send*` APIs.
- Inbound: client long-polls `getupdates` + `get_updates_buf`.
- Outbound: client `sendmessage` with echoed `context_token`.
- Media: `getuploadurl` → AES-128-ECB CDN → `sendmessage` refs.

## Layout (ownership)

| Package | Owns |
|---------|------|
| `protocol` | Wire types, path constants, fixtures |
| `ilink` | HTTP CGI, auth headers, rate/stale helpers; inject `Doer` |
| `auth` | QR login state machine |
| `state` | Account, sync cursor, context tokens, peer window/quota |
| `media` | AES-ECB, CDN, download/upload, SILK |
| `markdown` | Outbound text filter + rune chunking |
| `session` | Run loop, policy, Send*, typing, Manager |
| `internal/testutil` | `FakeTransport` and test helpers only |

- Dependency direction: lower packages must not import `session`. No import cycles.
- Do **not** expose `internal/` in the public API.

## Commands

```bash
go test ./... -count=1
go test -race ./...
go test ./session -run '^TestName$' -count=1 -v
```

- Format with `gofmt` / `goimports`. Do not commit repo-wide format-only churn.

## Product boundary (hard)

**DO**

- QR login, long-poll `getupdates`, `sendmessage`, CDN media, `context_token` glue.
- Peer session window / outbound quota, typing, multi-account `session.Manager`.

**DON'T**

- LLM / Agent runtime, tool orchestration, memory, business “who may chat” auth.
- OpenClaw channel registration, webhook servers, global bot singletons.
- Features that only exist to mirror a non-Go host framework.

## Business logic first — anti-port (hard)

- Implement from **protocol facts** and **product rules**, not from openclaw-weixin (or any non-Go) file trees, class graphs, or async pipelines.
- **Refactor freely for Go shape.** Preserve **observable behavior**: wire formats, policy outcomes, error semantics, tests/fixtures.
- Name after the domain (`TouchInbound`, `TryReserveOutbound`, `ParseInbound`), not after foreign symbols.
- Split by concern (policy / inbound parse / media send), **not** 1:1 with a TypeScript path layout.
- No adapter-of-adapter layers, empty interface buses, or noun-heavy `Manager`/`Service`/`Helper` stacks that hide real dependencies.

**Self-check:** If the TypeScript repo vanished, would this Go still be the natural design? If no → rewrite.

## Go style (hard)

- Early returns; no deep `else` pyramids.
- `context.Context` is the first parameter on I/O and long-running functions; honor cancel.
- Accept **small** interfaces at boundaries (`ilink.Doer`); **return concrete types**.
- Define interfaces where they are **used**, not “just in case”.
- Errors: package-prefixed messages; sentinels for policy (`ErrSessionWindow`, …); wrap with `%w`; compare with `errors.Is` / `errors.As`.
- Handle an error **once** at a boundary (log **or** return; be consistent).
- No `panic` for control flow in library packages.
- Use `time.Time` / `time.Duration` in Go logic; map protocol int-ms only at the edge.
- Prefer useful zero values; no DI frameworks; no Options explosion for simple constructors.
- Prefer the standard library. New dependencies need a clear gap (SILK via go-silk is the known exception).
- Logging: `log/slog` only.
- Comments explain **why** and non-obvious protocol/policy traps; never narrate the code.
- Every exported symbol has a godoc comment that starts with the name.

## Library rules (hard)

- **Outbound:** run reserve/policy **before** any `sendmessage`. No bypass paths.
- Default hard policy: session window + outbound quota (see README). Changing defaults is a product decision + test update.
- **Inbound media:** download/transcode may degrade; the message must still be delivered (keep tests aligned).
- HTTP must stay injectable for tests; production media-URL SSRF protections stay on.
- Multi-account is explicit (`Session` / `Manager`); no hidden process-global bot.

## Tests (hard)

- Prefer table-driven tests when they clarify cases.
- Drive **shipped APIs** and protocol behavior with `internal/testutil.FakeTransport` and `protocol` fixtures.
- Unit tests: **no real network**.
- Keep quota/chunk/reservation paths race-clean (`go test -race` when concurrency is touched).
- User-visible behavior change → update tests and README capability notes in the same change.

## Scope and safety

- Touch only files required for the task.
- Do not resurrect `examples/` or large design-doc trees unless asked.
- Never commit secrets, bot tokens, or personal state paths.
- Assume offline tests; do not hit production iLink from the suite.

## For AI agents

- Prefer one logical change per patch.
- After Go edits, run `go test ./... -count=1` (add `-race` if concurrency changed).
- Uncertain product scope → **ask** before inventing Agent/host features.
- Update this file when repository conventions change.
