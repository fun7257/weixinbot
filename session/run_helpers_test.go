package session

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/fun7257/weixinbot/ilink"
	"github.com/fun7257/weixinbot/internal/testutil"
	"github.com/fun7257/weixinbot/protocol"
	"github.com/fun7257/weixinbot/state"
)

func TestResolveAccountID(t *testing.T) {
	t.Setenv(envAccountID, "")

	tests := []struct {
		name    string
		arg     string
		env     string
		ids     []string
		want    string
		wantErr bool
	}{
		{name: "arg wins over env and list", arg: "from-arg", env: "from-env", ids: []string{"only"}, want: "from-arg"},
		{name: "env when arg empty", arg: "", env: "from-env", ids: []string{"only"}, want: "from-env"},
		{name: "arg trims space", arg: "  spaced  ", want: "spaced"},
		{name: "single listed account", arg: "", env: "", ids: []string{"only"}, want: "only"},
		{name: "empty store", arg: "", env: "", ids: nil, wantErr: true},
		{name: "multiple accounts", arg: "", env: "", ids: []string{"a", "b"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envAccountID, tc.env)
			st, err := state.NewStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range tc.ids {
				if err := st.RegisterAccountID(id); err != nil {
					t.Fatal(err)
				}
			}
			got, err := resolveAccountID(st, tc.arg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %q, want error", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q %v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestResolveTokenAccountID(t *testing.T) {
	t.Setenv(envAccountID, "")
	if got := resolveTokenAccountID(""); got != defaultTokenAccountID {
		t.Fatalf("empty → %q, want %q", got, defaultTokenAccountID)
	}
	t.Setenv(envAccountID, "from-env")
	if got := resolveTokenAccountID(""); got != "from-env" {
		t.Fatalf("env → %q", got)
	}
	if got := resolveTokenAccountID("from-arg"); got != "from-arg" {
		t.Fatalf("arg → %q", got)
	}
}

func TestRunTokenRequiresToken(t *testing.T) {
	err := RunToken(context.Background(), t.TempDir(), "acc", "", nil)
	if err == nil {
		t.Fatal("expected token required")
	}
}

func TestRunAccountReturnsOnCancel(t *testing.T) {
	ft := testutil.NewFakeTransport()
	ft.OnContains(protocol.PathNotifyStart, testutil.JSONResponder(200, protocol.NotifyResp{Ret: 0}, nil))
	ft.OnContains(protocol.PathNotifyStop, testutil.JSONResponder(200, protocol.NotifyResp{Ret: 0}, nil))
	ft.OnContains(protocol.PathGetUpdates, func(req *http.Request, body []byte) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})

	dir := t.TempDir()
	st, err := state.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterAccountID("bot-run"); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveAccount("bot-run", state.Account{Token: "tok", BaseURL: "https://ilink.test"}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- runAccount(ctx, dir, "bot-run", nil, ft.Client()) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunAccount did not return after cancel")
	}
}

func TestRunTokenPersistsAndReturnsOnCancel(t *testing.T) {
	t.Setenv(envAccountID, "")
	ft := testutil.NewFakeTransport()
	ft.OnContains(protocol.PathNotifyStart, testutil.JSONResponder(200, protocol.NotifyResp{Ret: 0}, nil))
	ft.OnContains(protocol.PathNotifyStop, testutil.JSONResponder(200, protocol.NotifyResp{Ret: 0}, nil))
	ft.OnContains(protocol.PathGetUpdates, func(req *http.Request, body []byte) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})

	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- runToken(ctx, dir, "", "secret-token", nil, ft.Client()) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunToken did not return after cancel")
	}

	st, err := state.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	acc, err := st.LoadAccount(defaultTokenAccountID)
	if err != nil {
		t.Fatal(err)
	}
	if acc.Token != "secret-token" {
		t.Fatalf("token %q", acc.Token)
	}
	if acc.BaseURL != ilink.DefaultBaseURL {
		t.Fatalf("BaseURL %q, want %q", acc.BaseURL, ilink.DefaultBaseURL)
	}
}

func TestLoginAndRunEmptyStoreDir(t *testing.T) {
	err := LoginAndRun(context.Background(), "", nil)
	if err == nil {
		t.Fatal("expected error")
	}
}
