package state_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tencent-weixin/weixinbot/state"
)

func TestSaveAccountRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	st, err := state.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}

	// Classic traversal: must not write outside store root.
	err = st.SaveAccount("../../outside", state.Account{Token: "leaked-token"})
	if err == nil {
		t.Fatal("expected error for traversal accountID")
	}

	// Outside file must not exist relative to parent of root.
	outside := filepath.Join(filepath.Dir(root), "outside.json")
	if _, err := os.Stat(outside); err == nil {
		t.Fatalf("token file escaped store root: %s", outside)
	}
	// Also ensure nothing named outside under root's parent accounts-style path.
	escaped := filepath.Clean(filepath.Join(root, "accounts", "..", "..", "outside.json"))
	if _, err := os.Stat(escaped); err == nil {
		// If this path resolves outside and exists, fail.
		absRoot, _ := filepath.Abs(root)
		absEsc, _ := filepath.Abs(escaped)
		if absEsc != absRoot && !hasPrefixDir(absEsc, absRoot) {
			t.Fatalf("escaped path exists: %s", absEsc)
		}
	}

	// Slash-containing ids rejected.
	if err := st.SaveAccount("a/b", state.Account{Token: "x"}); err == nil {
		t.Fatal("expected error for slash in accountID")
	}
	if err := st.SaveSyncBuf("../x", "buf"); err == nil {
		t.Fatal("expected error for sync buf traversal")
	}
	if err := st.SetContextToken("..\\evil", "u", "tok"); err == nil {
		t.Fatal("expected error for context token traversal")
	}
}

func TestSaveAccountWritesUnderAccountsOnly(t *testing.T) {
	root := t.TempDir()
	st, err := state.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	const id = "bot-im-bot"
	if err := st.SaveAccount(id, state.Account{Token: "tok-1"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "accounts", id+".json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected account file at %s: %v", path, err)
	}
	acc, err := st.LoadAccount(id)
	if err != nil {
		t.Fatal(err)
	}
	if acc.Token != "tok-1" {
		t.Fatalf("token %q", acc.Token)
	}
	if err := st.SaveSyncBuf(id, "cursor-9"); err != nil {
		t.Fatal(err)
	}
	buf, err := st.LoadSyncBuf(id)
	if err != nil || buf != "cursor-9" {
		t.Fatalf("sync buf %q err=%v", buf, err)
	}
}

func hasPrefixDir(path, dir string) bool {
	sep := string(filepath.Separator)
	return path == dir || len(path) > len(dir) && path[:len(dir)+1] == dir+sep
}
