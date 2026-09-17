package session

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/fun7257/weixinbot/auth"
	"github.com/fun7257/weixinbot/ilink"
	"github.com/fun7257/weixinbot/state"
)

const envAccountID = "ILINK_ACCOUNT_ID"

// defaultTokenAccountID is used by RunToken when neither the argument nor
// ILINK_ACCOUNT_ID is set.
const defaultTokenAccountID = "default"

// RunAccount opens storeDir, resolves accountID, then Open + Run.
// accountID is resolved as: argument → env ILINK_ACCOUNT_ID → the single
// ListAccountIDs entry → error if zero or multiple accounts remain.
func RunAccount(ctx context.Context, storeDir, accountID string, handler Handler) error {
	return runAccount(ctx, storeDir, accountID, handler, nil)
}

func runAccount(ctx context.Context, storeDir, accountID string, handler Handler, httpDoer ilink.Doer) error {
	store, err := state.NewStore(storeDir)
	if err != nil {
		return err
	}
	id, err := resolveAccountID(store, accountID)
	if err != nil {
		return err
	}
	return openAndRun(ctx, store, id, handler, httpDoer)
}

// RunToken persists token (BaseURL ilink.DefaultBaseURL) then Open + Run.
// token is required. accountID is resolved as: argument → env ILINK_ACCOUNT_ID → "default".
func RunToken(ctx context.Context, storeDir, accountID, token string, handler Handler) error {
	return runToken(ctx, storeDir, accountID, token, handler, nil)
}

func runToken(ctx context.Context, storeDir, accountID, token string, handler Handler, httpDoer ilink.Doer) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("session: token required")
	}
	id := resolveTokenAccountID(accountID)
	store, err := state.NewStore(storeDir)
	if err != nil {
		return err
	}
	if err := store.RegisterAccountID(id); err != nil {
		return err
	}
	if err := store.SaveAccount(id, state.Account{
		Token:   token,
		BaseURL: ilink.DefaultBaseURL,
	}); err != nil {
		return err
	}
	return openAndRun(ctx, store, id, handler, httpDoer)
}

// LoginAndRun runs auth.LoginQRStdin, then Open + Run for the logged-in account.
func LoginAndRun(ctx context.Context, storeDir string, handler Handler) error {
	return loginAndRun(ctx, storeDir, handler, nil)
}

func loginAndRun(ctx context.Context, storeDir string, handler Handler, httpDoer ilink.Doer) error {
	store, err := state.NewStore(storeDir)
	if err != nil {
		return err
	}
	res, err := auth.LoginQRStdin(ctx, store)
	if err != nil {
		return err
	}
	accountID := ""
	if res != nil {
		accountID = res.AccountID
	}
	if accountID == "" {
		accountID, err = resolveAccountID(store, "")
		if err != nil {
			return err
		}
	}
	return openAndRun(ctx, store, accountID, handler, httpDoer)
}

func openAndRun(ctx context.Context, store *state.Store, accountID string, handler Handler, httpDoer ilink.Doer) error {
	sess, err := Open(store, accountID, httpDoer, handler)
	if err != nil {
		return err
	}
	return sess.Run(ctx)
}

func resolveAccountID(store *state.Store, accountID string) (string, error) {
	if id := accountIDFromArgOrEnv(accountID); id != "" {
		return id, nil
	}
	ids, err := store.ListAccountIDs()
	if err != nil {
		return "", err
	}
	if len(ids) == 1 {
		return ids[0], nil
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("session: accountID required (set argument, ILINK_ACCOUNT_ID, or persist one account)")
	}
	return "", fmt.Errorf("session: accountID required: store has %d accounts", len(ids))
}

func resolveTokenAccountID(accountID string) string {
	if id := accountIDFromArgOrEnv(accountID); id != "" {
		return id
	}
	return defaultTokenAccountID
}

func accountIDFromArgOrEnv(accountID string) string {
	if id := strings.TrimSpace(accountID); id != "" {
		return id
	}
	return strings.TrimSpace(os.Getenv(envAccountID))
}
