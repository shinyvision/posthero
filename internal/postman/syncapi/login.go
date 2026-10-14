package syncapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shinyvision/posthero/internal/postman"
)

const CallbackURL = "postman://auth/callback"
const DefaultTokenURL = "https://app.getpostman.com/api"

// Session is a desktop login. RefreshToken and ExpiresAt are optional;
// without renewal metadata the service decides when sign-in is needed again.
// ExpiresAt belongs to the access token. Tokens must be stored as secrets.
type Session struct {
	Token        string    `json:"token,omitempty"`
	RefreshToken string    `json:"refreshToken,omitempty"`
	ExpiresAt    time.Time `json:"expiresAt"`
	UserID       int64     `json:"userId"`
	TeamID       int64     `json:"teamId,omitempty"`
	TeamDomain   string    `json:"teamDomain,omitempty"`
}

type LoginOptions struct {
	IdentityURL string
	TeamID      int64
	TeamDomain  string
}

// LoginFlow owns the secret for one browser handover. Postman's public
// cloud accepts only CallbackURL and uses hex, rather than base64, PKCE.
type LoginFlow struct {
	URL      string
	verifier string
	identity string
}

func BeginLogin(ctx context.Context, opts LoginOptions) (*LoginFlow, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	f := &LoginFlow{verifier: hex.EncodeToString(secret), identity: strings.TrimRight(orDefault(opts.IdentityURL, DefaultIdentityURL), "/")}
	hash := sha256.Sum256([]byte(f.verifier))
	body := map[string]any{"challenge": hex.EncodeToString(hash[:]), "redirect_uri": CallbackURL, "target": "accounts"}
	if opts.TeamID != 0 || opts.TeamDomain != "" {
		team := map[string]any{}
		if opts.TeamID != 0 {
			team["id"] = opts.TeamID
		}
		if opts.TeamDomain != "" {
			team["domain"] = opts.TeamDomain
		}
		body["authTarget"] = map[string]any{"team": team}
	}
	var out struct {
		RedirectURL string `json:"redirectUrl"`
	}
	if err := identityJSON(ctx, f.identity+"/api/public/pkce/exchange/init", body, &out); err != nil {
		return nil, err
	}
	u := out.RedirectURL
	if u != "" && !strings.Contains(u, "://") {
		u = "https://" + u
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return nil, errors.New("Postman sent an invalid sign-in address")
	}
	f.URL = u
	return f, nil
}

// AuthorizationCode accepts the callback link, the browser's success
// address, or a code copied from that page. Other URLs are never consumed.
func AuthorizationCode(input string) (string, error) {
	input = strings.TrimSpace(input)
	if len(input) == 0 || len(input) > 8192 {
		return "", errors.New("paste the sign-in code or callback link from Postman")
	}
	if strings.Contains(input, ":") || strings.Contains(input, "/") || strings.Contains(input, "?") {
		u, err := url.Parse(input)
		if err != nil || u.User != nil || u.Fragment != "" {
			return "", errors.New("invalid Postman callback link")
		}
		callback := u.Scheme == "postman" && u.Host == "auth" && u.Path == "/callback"
		success := u.Scheme == "https" && u.Host == "identity.getpostman.com" && u.Path == "/browser-auth/success"
		if !callback && !success {
			return "", errors.New("this is not a Postman sign-in callback")
		}
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil || len(q["code"]) != 1 || q.Get("error") != "" {
			return "", errors.New("Postman did not return a sign-in code")
		}
		input = q.Get("code")
	}
	if input == "" || strings.ContainsAny(input, " \t\r\n\x00") {
		return "", errors.New("invalid Postman sign-in code")
	}
	return input, nil
}

func (f *LoginFlow) Complete(ctx context.Context, input string) (Session, error) {
	code, err := AuthorizationCode(input)
	if err != nil {
		return Session{}, err
	}
	var out struct {
		Status string `json:"status"`
		Error  struct {
			Name string `json:"name"`
		} `json:"error"`
		Session struct {
			Token string `json:"token"`
		} `json:"session"`
		AccessToken  string `json:"accessToken"`
		RefreshToken struct {
			Token     string `json:"refresh_token"`
			ExpiresIn int64  `json:"expires_in"`
		} `json:"refreshToken"`
		SessionData struct {
			Identity struct {
				User   int64  `json:"user"`
				Team   int64  `json:"team"`
				Domain string `json:"domain"`
			} `json:"identity"`
		} `json:"sessionData"`
	}
	err = identityJSON(ctx, f.identity+"/api/handover/pkce/"+f.verifier+"/consume", map[string]string{"authorizationCode": code}, &out)
	if err != nil {
		return Session{}, err
	}
	// The identity service also reports failed handovers in HTTP 200
	// responses. Never display its raw error text: it can contain secrets.
	if out.Status == "error" {
		switch out.Error.Name {
		case "EntityNotFound", "InvalidAuthorizationCode":
			return Session{}, errors.New("the Postman sign-in code is invalid or has expired; start a new sign-in from posthero")
		default:
			return Session{}, errors.New("Postman couldn't complete the sign-in; start a new sign-in from posthero")
		}
	}
	s := Session{Token: orDefault(out.Session.Token, out.AccessToken), RefreshToken: out.RefreshToken.Token,
		UserID: out.SessionData.Identity.User, TeamID: out.SessionData.Identity.Team, TeamDomain: out.SessionData.Identity.Domain}
	if s.Token == "" {
		return Session{}, errors.New("Postman's sign-in response is missing access token; start a new sign-in from posthero")
	}
	if s.UserID == 0 {
		return Session{}, errors.New("Postman's sign-in response is missing user ID; start a new sign-in from posthero")
	}
	// Postman's desktop AuthCodeModal accepts the session token and identity
	// without refreshToken. Do not reject those logins or invent an expiry.
	if out.RefreshToken.ExpiresIn > 0 {
		s.ExpiresAt = time.Now().Add(time.Duration(out.RefreshToken.ExpiresIn) * time.Second)
	}
	return s, nil
}

func identityJSON(ctx context.Context, address string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewReader(raw))
	if err != nil {
		return errors.New("invalid Postman identity address")
	}
	req.Header.Set("Content-Type", "application/json")
	return loginRequest(req, out)
}

// loginRequest deliberately doesn't include request URLs, bodies or server
// error text in errors: the PKCE verifier and tokens may appear there.
func loginRequest(req *http.Request, out any) error {
	return loginRequestWithAgent(req, out, UserAgent)
}

func loginRequestWithAgent(req *http.Request, out any, agent string) error {
	req.Header.Set("User-Agent", agent)
	req.Header.Set("Accept", "application/json")
	c := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		if req.Context().Err() != nil {
			return req.Context().Err()
		}
		return errors.New("couldn't reach Postman's sign-in service")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		status := resp.StatusCode
		if status == 403 {
			status = 401
		}
		return &postman.Error{Status: status, Message: "Postman couldn't authorize the session (" + strconv.Itoa(resp.StatusCode) + ")"}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return errors.New("Postman sent an invalid sign-in response")
	}
	return nil
}

// RenewSession follows the desktop app's refresh exchange. /renewed-token
// is a separate acknowledgement that invalidates the old refresh token.
func RenewSession(ctx context.Context, s Session, tokenURL string) (Session, error) {
	return renewSession(ctx, s, tokenURL, UserAgent)
}

func renewSession(ctx context.Context, s Session, tokenURL, agent string) (Session, error) {
	if s.RefreshToken == "" || s.UserID == 0 {
		return Session{}, &postman.Error{Status: 401, Message: "sign in to Postman again"}
	}
	address := strings.TrimRight(orDefault(tokenURL, DefaultTokenURL), "/") + "/client-oauth2-refresh?user_id=" + strconv.FormatInt(s.UserID, 10)
	values := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {s.RefreshToken}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, strings.NewReader(values.Encode()))
	if err != nil {
		return Session{}, errors.New("invalid Postman renewal address")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("x-access-token", s.Token)
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Result       *bool  `json:"result"`
	}
	if err := loginRequestWithAgent(req, &out, agent); err != nil {
		return Session{}, err
	}
	if out.Result != nil && !*out.Result {
		return Session{}, &postman.Error{Status: 401, Message: "Postman's session has ended; sign in again"}
	}
	if out.AccessToken == "" || out.ExpiresIn <= 0 {
		return Session{}, fmt.Errorf("Postman sent an incomplete renewed session")
	}
	s.Token = out.AccessToken
	if out.RefreshToken != "" {
		s.RefreshToken = out.RefreshToken
	}
	s.ExpiresAt = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	return s, nil
}
