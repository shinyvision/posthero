package syncapi

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shinyvision/posthero/internal/postman"
)

// NewSession renews the browser session as needed. OnSession, when set,
// must durably save a rotated session before it is used for requests.
func NewSession(session Session, opts Options) *Client {
	c := New(session.Token, opts)
	c.session = &session
	return c
}

// Refresh renews a session that expires within a day. The app also calls
// it while idle so a long-running watch stays signed in.
func (c *Client) Refresh(ctx context.Context) error {
	_, err := c.sessionToken(ctx, "")
	return err
}

// rejected is the token a read was explicitly refused for. Concurrent
// reads share one renewal; a write is never replayed here.
func (c *Client) sessionToken(ctx context.Context, rejected string) (string, error) {
	select {
	case c.authGate <- struct{}{}:
		defer func() { <-c.authGate }()
	case <-ctx.Done():
		return "", ctx.Err()
	case <-c.ctx.Done():
		return "", c.ctx.Err()
	}
	if c.ctx.Err() != nil {
		return "", c.ctx.Err()
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	defer func() { stop(); cancel() }()
	if c.session == nil {
		return c.token, nil
	}
	// A failed disk/keyring write must retry saving the rotated token,
	// never rotate the old refresh token a second time.
	if c.unsaved {
		if err := c.saveSession(); err != nil {
			return "", err
		}
	}
	s := *c.session
	refused := rejected != "" && rejected == s.Token
	if s.RefreshToken == "" {
		if refused || (!s.ExpiresAt.IsZero() && !time.Now().Before(s.ExpiresAt)) {
			return "", &postman.Error{Status: 401, Message: "Postman's session has ended; sign in again"}
		}
		return c.token, nil
	}
	due := !s.ExpiresAt.IsZero() && time.Until(s.ExpiresAt) <= 24*time.Hour
	if due || refused {
		if c.renewErr != nil && time.Now().Before(c.renewAfter) {
			return "", c.renewErr
		}
		next, err := renewSession(ctx, s, c.opts.TokenURL, userAgent(c.opts.Version))
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				c.renewErr, c.renewAfter = err, time.Now().Add(time.Minute)
			}
			return "", err
		}
		c.renewErr = nil
		c.session, c.token, c.unsaved = &next, next.Token, true
		if err := c.saveSession(); err != nil {
			return "", err
		}
	}
	return c.token, nil
}

func (c *Client) saveSession() error {
	if c.opts.OnSession != nil {
		if err := c.opts.OnSession(*c.session); err != nil {
			return fmt.Errorf("couldn't keep the renewed Postman session: %w", err)
		}
	}
	c.unsaved = false
	return nil
}
