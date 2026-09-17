// Command login-qr runs auth.LoginQRStdin and prints the persisted AccountID.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/fun7257/weixinbot/auth"
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	res, err := auth.LoginQRStdin(ctx, store)
	if err != nil {
		log.Fatal(err)
	}
	if res == nil {
		log.Fatal("login returned no result")
	}
	if res.AlreadyConnected {
		fmt.Printf("already connected account %s\n", res.AccountID)
		return
	}
	fmt.Printf("logged in account %s\n", res.AccountID)
}
