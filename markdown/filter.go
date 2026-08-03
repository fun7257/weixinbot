// Package markdown provides outbound text filtering for WeChat-safe plain output.
// Ported from the TypeScript StreamingMarkdownFilter core rules.
package markdown

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Filter applies the streaming markdown filter to a complete string (feed+flush).
func Filter(s string) string {
	var f StreamingFilter
	return f.Feed(s) + f.Flush()
}

// StreamingFilter is a character-level state machine that strips unsupported
// markdown constructs while preserving code fences, tables, bold, and non-CJK italics.
type StreamingFilter struct {
	buf     string
	fence   bool
	sol     bool
	inl     *inlineAcc
	started bool
}

type inlineAcc struct {
	typ string // image | bold3 | italic | ubold3 | uitalic
	acc string
}

// Feed consumes a delta and returns emit-able filtered text.
func (f *StreamingFilter) Feed(delta string) string {
	if !f.started {
		f.sol = true
		f.started = true
	}
	f.buf += delta
	return f.pump(false)
}

// Flush ends the stream and emits any held partial markers.
func (f *StreamingFilter) Flush() string {
	if !f.started {
		return ""
	}
	return f.pump(true)
}

func (f *StreamingFilter) pump(eof bool) string {
	var out strings.Builder
	for f.buf != "" {
		sLen := len(f.buf)
		sSol, sFence := f.sol, f.fence
		sInlTyp, sInlAcc, sInlNil := "", "", true
		if f.inl != nil {
			sInlNil, sInlTyp, sInlAcc = false, f.inl.typ, f.inl.acc
		}
		var chunk string
		switch {
		case f.fence:
			chunk = f.pumpFence(eof)
		case f.inl != nil:
			chunk = f.pumpInline(eof)
		case f.sol:
			chunk = f.pumpSOL(eof)
		default:
			chunk = f.pumpBody(eof)
		}
		out.WriteString(chunk)
		inlSame := (f.inl == nil) == sInlNil
		if f.inl != nil && !sInlNil {
			inlSame = f.inl.typ == sInlTyp && f.inl.acc == sInlAcc
		}
		if len(f.buf) == sLen && f.sol == sSol && f.fence == sFence && inlSame {
			break
		}
	}
	if eof && f.inl != nil {
		markers := map[string]string{"image": "![", "bold3": "***", "italic": "*", "ubold3": "___", "uitalic": "_"}
		out.WriteString(markers[f.inl.typ])
		out.WriteString(f.inl.acc)
		f.inl = nil
	}
	return out.String()
}

func (f *StreamingFilter) pumpFence(eof bool) string {
	if f.sol {
		if len(f.buf) < 3 && !eof {
			return ""
		}
		if strings.HasPrefix(f.buf, "```") {
			if nl := indexFrom(f.buf, '\n', 3); nl != -1 {
				f.fence = false
				line := f.buf[:nl+1]
				f.buf = f.buf[nl+1:]
				f.sol = true
				return line
			}
			if eof {
				f.fence = false
				line := f.buf
				f.buf = ""
				return line
			}
			return ""
		}
		f.sol = false
	}
	if nl := strings.IndexByte(f.buf, '\n'); nl != -1 {
		chunk := f.buf[:nl+1]
		f.buf = f.buf[nl+1:]
		f.sol = true
		return chunk
	}
	chunk := f.buf
	f.buf = ""
	return chunk
}

func (f *StreamingFilter) pumpSOL(eof bool) string {
	b := f.buf
	if b == "" {
		return ""
	}
	if b[0] == '\n' {
		f.buf = b[1:]
		return "\n"
	}
	if b[0] == '`' {
		if len(b) < 3 && !eof {
			return ""
		}
		if strings.HasPrefix(b, "```") {
			if nl := indexFrom(b, '\n', 3); nl != -1 {
				f.fence = true
				line := b[:nl+1]
				f.buf = b[nl+1:]
				f.sol = true
				return line
			}
			if eof {
				f.buf = ""
				return b
			}
			return ""
		}
		f.sol = false
		return ""
	}
	if b[0] == '>' {
		f.sol = false
		return ""
	}
	if b[0] == '#' {
		n := 0
		for n < len(b) && b[n] == '#' {
			n++
		}
		if n == len(b) && !eof {
			return ""
		}
		if n >= 5 && n <= 6 && n < len(b) && b[n] == ' ' {
			f.buf = b[n+1:]
			f.sol = false
			return ""
		}
		f.sol = false
		return ""
	}
	if b[0] == ' ' || b[0] == '\t' {
		onlyWS := true
		for i := 0; i < len(b); i++ {
			if b[i] != ' ' && b[i] != '\t' {
				onlyWS = false
				break
			}
		}
		if onlyWS && !eof {
			return ""
		}
		f.sol = false
		return ""
	}
	if b[0] == '-' || b[0] == '*' || b[0] == '_' {
		ch := b[0]
		j := 0
		for j < len(b) && (b[j] == ch || b[j] == ' ') {
			j++
		}
		if j == len(b) && !eof {
			return ""
		}
		if j == len(b) || b[j] == '\n' {
			count := 0
			for k := 0; k < j; k++ {
				if b[k] == ch {
					count++
				}
			}
			if count >= 3 {
				if j < len(b) {
					f.buf = b[j+1:]
					f.sol = true
					return b[:j+1]
				}
				f.buf = ""
				return b
			}
		}
		f.sol = false
		return ""
	}
	f.sol = false
	return ""
}

func (f *StreamingFilter) pumpBody(eof bool) string {
	// Port of TS pumpBody: scan for triggers; skip single ~; skip ** pairs as passthrough
	// by advancing without emitting the scan position incorrectly.
	// We emit the prefix before a trigger, then switch state.
	i := 0
	// Track bytes to skip (tildes and ** markers that should be dropped? TS skips ~ chars from output)
	// TS: on ~ it does i++ continue without adding to out — but out is slice of buf which includes ~.
	// Looking carefully: out accumulates via slice of buf only at trigger points from 0..i.
	// When it hits ~ it advances i without returning, so ~ is included in eventual out slice.
	// Actually: `if (c === "~") { i++; continue; }` — when later out += buf.slice(0,i) the ~ is included
	// if a later trigger fires. If end of loop, out += buf with hold — includes ~.
	// Wait, the continue skips adding but i points after ~, so slice(0,i) includes ~.
	// So ~ is NOT removed? That seems wrong for strikethrough ~~.
	// Looking again... for ~~text~~, first ~ at i, continue i=1; second ~ at i=1, continue i=2;
	// then more... at end out includes the ~~. So ~~ is preserved?
	// Actually markdown-filter docs say strike is stripped inside fences only.
	// Migrated tests don't cover ~~.
	// We'll match TS behavior literally.

	for i < len(f.buf) {
		c := f.buf[i]
		if c == '\n' {
			out := f.buf[:i+1]
			f.buf = f.buf[i+1:]
			f.sol = true
			return out
		}
		if c == '!' && i+1 < len(f.buf) && f.buf[i+1] == '[' {
			out := f.buf[:i]
			f.buf = f.buf[i+2:]
			f.inl = &inlineAcc{typ: "image"}
			return out
		}
		if c == '~' {
			i++
			continue
		}
		if c == '*' {
			if i+2 < len(f.buf) && f.buf[i+1] == '*' && f.buf[i+2] == '*' {
				out := f.buf[:i]
				f.buf = f.buf[i+3:]
				f.inl = &inlineAcc{typ: "bold3"}
				return out
			}
			if i+1 < len(f.buf) && f.buf[i+1] == '*' {
				i += 2
				continue
			}
			if i+1 < len(f.buf) && f.buf[i+1] != ' ' && f.buf[i+1] != '\n' {
				out := f.buf[:i]
				f.buf = f.buf[i+1:]
				f.inl = &inlineAcc{typ: "italic"}
				return out
			}
			i++
			continue
		}
		if c == '_' {
			if i+2 < len(f.buf) && f.buf[i+1] == '_' && f.buf[i+2] == '_' {
				out := f.buf[:i]
				f.buf = f.buf[i+3:]
				f.inl = &inlineAcc{typ: "ubold3"}
				return out
			}
			if i+1 < len(f.buf) && f.buf[i+1] == '_' {
				i += 2
				continue
			}
			if i+1 < len(f.buf) && f.buf[i+1] != ' ' && f.buf[i+1] != '\n' {
				out := f.buf[:i]
				f.buf = f.buf[i+1:]
				f.inl = &inlineAcc{typ: "uitalic"}
				return out
			}
			i++
			continue
		}
		i++
	}

	hold := 0
	if !eof {
		switch {
		case strings.HasSuffix(f.buf, "**"), strings.HasSuffix(f.buf, "__"):
			hold = 2
		case strings.HasSuffix(f.buf, "*"), strings.HasSuffix(f.buf, "_"), strings.HasSuffix(f.buf, "!"):
			hold = 1
		}
	}
	out := f.buf[:len(f.buf)-hold]
	if hold > 0 {
		f.buf = f.buf[len(f.buf)-hold:]
	} else {
		f.buf = ""
	}
	return out
}

func (f *StreamingFilter) pumpInline(eof bool) string {
	if f.inl == nil {
		return ""
	}
	f.inl.acc += f.buf
	f.buf = ""
	switch f.inl.typ {
	case "bold3":
		if idx := strings.Index(f.inl.acc, "***"); idx != -1 {
			content := f.inl.acc[:idx]
			f.buf = f.inl.acc[idx+3:]
			f.inl = nil
			if containsCJK(content) {
				return content
			}
			return "***" + content + "***"
		}
	case "ubold3":
		if idx := strings.Index(f.inl.acc, "___"); idx != -1 {
			content := f.inl.acc[:idx]
			f.buf = f.inl.acc[idx+3:]
			f.inl = nil
			if containsCJK(content) {
				return content
			}
			return "___" + content + "___"
		}
	case "italic":
		for j := 0; j < len(f.inl.acc); j++ {
			if f.inl.acc[j] == '\n' {
				r := "*" + f.inl.acc[:j+1]
				f.buf = f.inl.acc[j+1:]
				f.inl = nil
				f.sol = true
				return r
			}
			if f.inl.acc[j] == '*' {
				if j+1 < len(f.inl.acc) && f.inl.acc[j+1] == '*' {
					j++
					continue
				}
				content := f.inl.acc[:j]
				f.buf = f.inl.acc[j+1:]
				f.inl = nil
				if containsCJK(content) {
					return content
				}
				return "*" + content + "*"
			}
		}
	case "uitalic":
		for j := 0; j < len(f.inl.acc); j++ {
			if f.inl.acc[j] == '\n' {
				r := "_" + f.inl.acc[:j+1]
				f.buf = f.inl.acc[j+1:]
				f.inl = nil
				f.sol = true
				return r
			}
			if f.inl.acc[j] == '_' {
				if j+1 < len(f.inl.acc) && f.inl.acc[j+1] == '_' {
					j++
					continue
				}
				content := f.inl.acc[:j]
				f.buf = f.inl.acc[j+1:]
				f.inl = nil
				if containsCJK(content) {
					return content
				}
				return "_" + content + "_"
			}
		}
	case "image":
		cb := strings.IndexByte(f.inl.acc, ']')
		if cb == -1 {
			return ""
		}
		if cb+1 >= len(f.inl.acc) {
			return ""
		}
		if f.inl.acc[cb+1] != '(' {
			r := "![" + f.inl.acc[:cb+1]
			f.buf = f.inl.acc[cb+1:]
			f.inl = nil
			return r
		}
		rest := f.inl.acc[cb+2:]
		if cp := strings.IndexByte(rest, ')'); cp != -1 {
			f.buf = rest[cp+1:]
			f.inl = nil
			return ""
		}
		return ""
	}
	_ = eof
	return ""
}

func indexFrom(s string, c byte, from int) int {
	if from >= len(s) {
		return -1
	}
	i := strings.IndexByte(s[from:], c)
	if i < 0 {
		return -1
	}
	return from + i
}

var cjkRe = regexp.MustCompile(`[\x{2E80}-\x{9FFF}\x{AC00}-\x{D7AF}\x{F900}-\x{FAFF}]`)

func containsCJK(text string) bool {
	return cjkRe.MatchString(text)
}

// ChunkByRunes splits s into pieces of at most limit runes (not bytes).
func ChunkByRunes(s string, limit int) []string {
	if limit <= 0 {
		if s == "" {
			return nil
		}
		return []string{s}
	}
	if utf8.RuneCountInString(s) <= limit {
		if s == "" {
			return nil
		}
		return []string{s}
	}
	var out []string
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= limit {
			out = append(out, b.String())
			b.Reset()
			n = 0
		}
		b.WriteRune(r)
		n++
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}
