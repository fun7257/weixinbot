package state_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tencent-weixin/weixinbot/state"
)

func TestPeerStateTouchAndIncrPersist(t *testing.T) {
	root := t.TempDir()
	st, err := state.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	const account = "bot-peer"
	const user = "user@im.wechat"
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := st.TouchInbound(account, user, "ctx-1", at); err != nil {
		t.Fatal(err)
	}
	ps, err := st.GetPeer(account, user)
	if err != nil {
		t.Fatal(err)
	}
	if ps.ContextToken != "ctx-1" || ps.OutboundCount != 0 {
		t.Fatalf("%+v", ps)
	}
	if !ps.LastInboundAt.Equal(at) && ps.LastInboundAt.Unix() != at.Unix() {
		t.Fatalf("time %v want %v", ps.LastInboundAt, at)
	}
	if st.GetContextToken(account, user) != "ctx-1" {
		t.Fatal("context token not synced")
	}
	n, err := st.IncrOutbound(account, user)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	n, _ = st.IncrOutbound(account, user)
	if n != 2 {
		t.Fatal(n)
	}

	// restart store
	st2, err := state.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	ps2, err := st2.GetPeer(account, user)
	if err != nil {
		t.Fatal(err)
	}
	if ps2.OutboundCount != 2 || ps2.ContextToken != "ctx-1" {
		t.Fatalf("%+v", ps2)
	}
	// inbound resets
	if err := st2.TouchInbound(account, user, "ctx-2", at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	ps3, _ := st2.GetPeer(account, user)
	if ps3.OutboundCount != 0 || ps3.ContextToken != "ctx-2" {
		t.Fatalf("%+v", ps3)
	}
}

func TestPeerStatePathSanitize(t *testing.T) {
	root := t.TempDir()
	st, err := state.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.TouchInbound("../evil", "u", "t", time.Now()); err == nil {
		t.Fatal("expected reject")
	}
	// ensure no escape
	outside := filepath.Join(filepath.Dir(root), "evil.peer-state.json")
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("escaped")
	}
}

func TestClearStaleAccountsForUserID(t *testing.T) {
	root := t.TempDir()
	st, err := state.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.RegisterAccountID("old-bot")
	_ = st.SaveAccount("old-bot", state.Account{Token: "old", UserID: "uid-1"})
	_ = st.SetContextToken("old-bot", "peer", "tok")
	_ = st.RegisterAccountID("new-bot")
	_ = st.SaveAccount("new-bot", state.Account{Token: "new", UserID: "uid-1"})
	if err := st.ClearStaleAccountsForUserID("new-bot", "uid-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LoadAccount("old-bot"); err == nil {
		t.Fatal("old account should be deleted")
	}
	if _, err := st.LoadAccount("new-bot"); err != nil {
		t.Fatal(err)
	}
}
