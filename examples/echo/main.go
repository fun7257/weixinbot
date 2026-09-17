// Command echo is the primary weixinbot demo: OpenWith + WithTyping + SendText.
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/fun7257/weixinbot/session"
	"github.com/fun7257/weixinbot/state"
)

func main() {
	stateDir := os.Getenv("WEIXINBOT_STATE")
	if stateDir == "" {
		stateDir = filepath.Join(os.Getenv("HOME"), ".weixinbot")
	}
	store, err := state.NewStore(stateDir)
	if err != nil {
		log.Fatal(err)
	}

	accountID := os.Getenv("ILINK_ACCOUNT_ID")
	if accountID == "" {
		ids, err := store.ListAccountIDs()
		if err != nil {
			log.Fatal(err)
		}
		if len(ids) == 1 {
			accountID = ids[0]
		} else {
			log.Fatal("set ILINK_ACCOUNT_ID or persist a single account (see examples/login-qr)")
		}
	}

	sess, err := session.OpenWith(store, accountID, nil, func(s *session.Session) session.Handler {
		return func(ctx context.Context, msg session.InboundMessage) error {
			return s.WithTyping(ctx, msg.FromUserID, func(ctx context.Context) error {
				return s.SendText(ctx, msg.FromUserID, "echo: "+msg.Text)
			})
		}
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := sess.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
