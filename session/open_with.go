package session

import (
	"github.com/fun7257/weixinbot/ilink"
	"github.com/fun7257/weixinbot/state"
)

// OpenWith is Open plus a bind callback so the Handler can close over *Session
// (Send*, WithTyping) without a forward-declared variable.
func OpenWith(store *state.Store, accountID string, httpDoer ilink.Doer, bind func(*Session) Handler) (*Session, error) {
	sess, err := Open(store, accountID, httpDoer, nil)
	if err != nil {
		return nil, err
	}
	if bind != nil {
		sess.opts.Handler = bind(sess)
	}
	return sess, nil
}
