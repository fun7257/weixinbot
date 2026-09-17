package auth

import (
	"context"
	"fmt"
	"os"

	"github.com/fun7257/weixinbot/state"
)

// LoginQROptions configures LoginQR.
type LoginQROptions struct {
	Options
	// SessionKey is passed to StartQR; empty lets StartQR generate one.
	SessionKey string
	// OnQR is invoked once after StartQR succeeds (typically to display QRCodeURL).
	OnQR func(QRStart)
}

// LoginQR runs StartQR, optional OnQR, WaitLogin, then CompleteLogin.
// WaitLogin errors skip CompleteLogin so a failed poll does not persist credentials.
func LoginQR(ctx context.Context, store *state.Store, opts LoginQROptions) (*LoginResult, error) {
	if store == nil {
		return nil, fmt.Errorf("auth: nil store")
	}
	start, err := StartQR(ctx, opts.SessionKey, opts.Options)
	if err != nil {
		return nil, err
	}
	if opts.OnQR != nil {
		opts.OnQR(*start)
	}
	res, err := WaitLogin(ctx, start.SessionKey, opts.Options)
	if err != nil {
		return res, err
	}
	if err := CompleteLogin(store, res); err != nil {
		return res, err
	}
	return res, nil
}

// LoginQRStdin is LoginQR with StdinVerifyCode(os.Stdin, os.Stderr) and the QR URL printed to stderr.
func LoginQRStdin(ctx context.Context, store *state.Store) (*LoginResult, error) {
	return LoginQR(ctx, store, LoginQROptions{
		Options: Options{
			VerifyCode: StdinVerifyCode(os.Stdin, os.Stderr),
		},
		OnQR: func(qr QRStart) {
			fmt.Fprintf(os.Stderr, "Scan QR: %s\n", qr.QRCodeURL)
		},
	})
}
