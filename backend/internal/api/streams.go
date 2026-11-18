package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
)

// streamRecheck is how often an open stream (exec, logs, stats, events, live metrics)
// checks that its credential and its user's access still hold (Options.StreamRecheck).
const streamRecheck = time.Minute

// errRevoked says the request's user may no longer have what it opened: the session or
// token is gone (logout, expiry, disabled or deleted user, token deleted), the role
// changed or the environment was taken away.
var errRevoked = errors.New("access revoked")

type recheckKey struct{}

// recheckFunc runs the request's credential lookup again and reports errRevoked unless
// it still finds the same user with the same role.
type recheckFunc func(ctx context.Context) error

func withRecheck(ctx context.Context, f recheckFunc) context.Context {
	return context.WithValue(ctx, recheckKey{}, f)
}

// recheckFor returns the recheckFunc of the request r, which lookup authenticated as u.
func recheckFor(r *http.Request, u auth.User, lookup func(*http.Request) (auth.User, error)) recheckFunc {
	return func(ctx context.Context) error {
		now, err := lookup(r.WithContext(ctx))
		if errors.Is(err, auth.ErrNoSession) {
			return errRevoked
		}
		if err != nil {
			return err
		}
		if now.ID != u.ID || now.Role != u.Role {
			return errRevoked
		}
		return nil
	}
}

// streamAllowed repeats the checks a stream in environment env passed when it opened:
// the credential still finds the same user and role, and a user (not an admin) may
// still use env. It reports errRevoked when one of them no longer holds.
func (s *server) streamAllowed(ctx context.Context, env int64) error {
	recheck, ok := ctx.Value(recheckKey{}).(recheckFunc)
	u, uok := userFrom(ctx)
	if !ok || !uok {
		return errors.New("no credential in the request")
	}
	if err := recheck(ctx); err != nil {
		return err
	}
	if isAdmin(u) {
		return nil
	}
	allowed, err := s.envs.HasAccess(ctx, u.ID, env)
	if err != nil {
		return fmt.Errorf("environment access: %w", err)
	}
	if !allowed {
		return errRevoked
	}
	return nil
}

// keepAlive pings a stream's client until ctx is done and cancels the stream when a ping
// goes unanswered. Every s.streamRecheck it also checks that the stream is still allowed
// (streamAllowed), and closes it when not: authentication runs only when the socket
// opens, so without this a stream would outlive a logout, a disabled account or a
// withdrawn environment.
func (s *server) keepAlive(ctx context.Context, cancel context.CancelFunc, ws *websocket.Conn, env int64) {
	ping := time.NewTicker(logPingInterval)
	defer ping.Stop()
	recheck := time.NewTicker(s.streamRecheck)
	defer recheck.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			pctx, pcancel := context.WithTimeout(ctx, logPingTimeout)
			err := ws.Ping(pctx)
			pcancel()
			if err != nil {
				cancel()
				return
			}
		case <-recheck.C:
			cctx, ccancel := context.WithTimeout(ctx, logPingTimeout)
			err := s.streamAllowed(cctx, env)
			ccancel()
			if err == nil {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			u, _ := userFrom(ctx)
			code, reason := websocket.StatusPolicyViolation, errRevoked.Error()
			if errors.Is(err, errRevoked) {
				s.log.Info("stream closed: access revoked", "env", env, "user", u.Username)
			} else {
				// Fail closed: a stream that can't be checked doesn't go on.
				s.log.Error("stream recheck", "env", env, "user", u.Username, "err", err)
				code, reason = websocket.StatusInternalError, "internal error"
			}
			// Close before cancelling, so the client gets the reason rather than a
			// connection dropped by a cancelled write.
			_ = ws.Close(code, reason)
			cancel()
			return
		}
	}
}
