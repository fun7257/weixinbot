package markdown_test

import (
	"strings"
	"testing"

	"github.com/fun7257/weixinbot/markdown"
)

func TestFilterTable(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello world", "hello world"},
		{"image removed", "![alt](url)", ""},
		{"preserves bold and non-CJK italic", "**bold** and *italic*", "**bold** and *italic*"},
		{"code fence", "before\n```js\nconst x = 1;\n```\nafter", "before\n```js\nconst x = 1;\n```\nafter"},
		{"table", "结果如下：\n| A | B |\n|---|---|\n| 1 | 2 |\n完毕。", "结果如下：\n| A | B |\n|---|---|\n| 1 | 2 |\n完毕。"},
		{"cjk italic strips markers", "*你好*", "你好"},
		{"h5 heading strip", "##### title\nbody", "title\nbody"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := markdown.Filter(tt.in)
			if got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestChunkByRunes(t *testing.T) {
	s := strings.Repeat("你", 4500)
	parts := markdown.ChunkByRunes(s, 4000)
	if len(parts) != 2 {
		t.Fatalf("parts %d", len(parts))
	}
	if len([]rune(parts[0])) != 4000 || len([]rune(parts[1])) != 500 {
		t.Fatalf("sizes %d %d", len([]rune(parts[0])), len([]rune(parts[1])))
	}
	if parts[0]+parts[1] != s {
		t.Fatal("concat")
	}
}
