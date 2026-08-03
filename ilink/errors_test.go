package ilink_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/fun7257/weixinbot/ilink"
	"github.com/fun7257/weixinbot/protocol"
)

func TestIsRateLimitedAndIsStaleToken(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		rate       bool
		stale      bool
	}{
		{"nil", nil, false, false},
		{"plain", errors.New("nope"), false, false},
		{"ret-2", &ilink.APIError{Ret: -2, ErrMsg: "x"}, true, false},
		{"errcode-2", &ilink.APIError{ErrCode: -2}, true, false},
		{"msg rate limited", &ilink.APIError{Ret: 1, ErrMsg: "Rate Limited"}, true, false},
		{"stale ret", &ilink.APIError{Ret: protocol.StaleTokenErrCode}, false, true},
		{"stale errcode", &ilink.APIError{ErrCode: protocol.StaleTokenErrCode}, false, true},
		{"wrapped rate", fmt.Errorf("wrap: %w", &ilink.APIError{Ret: -2}), true, false},
		{"wrapped stale", fmt.Errorf("wrap: %w", &ilink.APIError{ErrCode: -14}), false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ilink.IsRateLimited(tt.err); got != tt.rate {
				t.Fatalf("IsRateLimited=%v want %v", got, tt.rate)
			}
			if got := ilink.IsStaleToken(tt.err); got != tt.stale {
				t.Fatalf("IsStaleToken=%v want %v", got, tt.stale)
			}
			if tt.err != nil && (tt.rate || tt.stale) {
				var ae *ilink.APIError
				if !errors.As(tt.err, &ae) {
					t.Fatal("errors.As failed")
				}
			}
		})
	}
}
