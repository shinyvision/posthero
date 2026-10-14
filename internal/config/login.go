package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strings"

	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/postman/syncapi"
	"github.com/shinyvision/posthero/internal/secret"
)

// Login selects either the public API (an API key) or the sync service
// (a browser session). Older API-key-only login files still work.
type Login struct {
	// APIKey is kept in the system keyring when there is one, and in the
	// login file (readable only by the user) otherwise.
	APIKey  string
	Session *syncapi.Session
	// From says where the login came from: "posthero" or "environment".
	From string
}

const keyringID = "postman-api-key"
const sessionKeyringID = "postman-session-v1"

type loginFile struct {
	APIKey         string           `json:"apiKey,omitempty"`
	Keyring        bool             `json:"keyring,omitempty"`
	Session        *syncapi.Session `json:"session,omitempty"`
	SessionKeyring bool             `json:"sessionKeyring,omitempty"`
}

// LoadLogin finds the login: posthero's own first, then
// $POSTMAN_API_KEY. ok is false when there is none.
func LoadLogin() (Login, bool, error) {
	path, err := configPath("login.json")
	if err != nil {
		return Login{}, false, err
	}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		var f loginFile
		if err := json.Unmarshal(data, &f); err != nil {
			return Login{}, false, err
		}
		l := Login{APIKey: f.APIKey, From: "posthero"}
		if f.Session != nil || f.SessionKeyring {
			l.Session = f.Session
			if f.SessionKeyring {
				raw, err := secret.Get(sessionKeyringID)
				if err == nil {
					err = json.Unmarshal([]byte(raw), &l.Session)
				}
				if err != nil {
					return Login{}, false, err
				}
			}
			if l.Session == nil || l.Session.Token == "" || l.Session.UserID == 0 {
				return Login{}, false, errors.New("saved Postman session is incomplete; sign in again")
			}
			return l, true, nil
		}
		if f.Keyring {
			key, err := secret.Get(keyringID)
			if err != nil {
				if env, ok := envLogin(); ok {
					return env, true, err
				}
				return l, false, err
			}
			l.APIKey = key
		}
		return l, l.APIKey != "", nil
	case !errors.Is(err, fs.ErrNotExist):
		return Login{}, false, err
	}
	l, ok := envLogin()
	return l, ok, nil
}

// SaveSession keeps both tokens together, so rotating them cannot leave a
// new access token paired with an old refresh token. The file contains
// only metadata when the keyring works, and has mode 0600 otherwise.
func SaveSession(s syncapi.Session) (string, error) {
	if s.Token == "" || s.UserID == 0 {
		return "", errors.New("cannot save an incomplete Postman session")
	}
	path, err := configPath("login.json")
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	f := loginFile{Session: &s}
	note := ""
	if err := secret.Set(sessionKeyringID, string(raw)); err == nil {
		meta := s
		meta.Token, meta.RefreshToken = "", ""
		f.Session, f.SessionKeyring = &meta, true
	} else {
		note = "no system keyring here, so the session is kept in " + path + " (readable only by you)"
	}
	data, err := jsonx.MarshalIndent(f, "", "  ")
	if err != nil {
		return "", err
	}
	return note, writeFile(path, append(data, '\n'), 0o600)
}

func envLogin() (Login, bool) {
	key := strings.TrimSpace(os.Getenv("POSTMAN_API_KEY"))
	return Login{APIKey: key, From: "environment"}, key != ""
}

// SaveLogin keeps the API key: in the system keyring when there is one. It
// returns a note when the key had to go in the file.
func SaveLogin(key string) (string, error) {
	path, err := configPath("login.json")
	if err != nil {
		return "", err
	}
	f := loginFile{}
	note := ""
	if err := secret.Set(keyringID, key); err == nil {
		f.Keyring = true
	} else {
		f.APIKey = key
		note = "no system keyring here, so the API key is kept in " + path + " (readable only by you)"
	}
	data, err := jsonx.MarshalIndent(f, "", "  ")
	if err != nil {
		return "", err
	}
	return note, writeFile(path, append(data, '\n'), 0o600)
}

// ForgetLogin removes posthero's stored login.
func ForgetLogin() error {
	path, err := configPath("login.json")
	if err != nil {
		return err
	}
	_ = secret.Delete(keyringID)
	_ = secret.Delete(sessionKeyringID)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
