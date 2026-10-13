package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strings"

	"github.com/shinyvision/posthero/internal/jsonx"
	"github.com/shinyvision/posthero/internal/secret"
)

// Login is how posthero signs in to the Postman API: an API key.
type Login struct {
	// APIKey is kept in the system keyring when there is one, and in the
	// login file (readable only by the user) otherwise.
	APIKey string
	// From says where the key came from: "posthero" or "environment".
	From string
}

const keyringID = "postman-api-key"

type loginFile struct {
	APIKey  string `json:"apiKey,omitempty"`
	Keyring bool   `json:"keyring,omitempty"`
}

// LoadLogin finds the API key: posthero's own first, then
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

// ForgetLogin removes posthero's stored API key.
func ForgetLogin() error {
	path, err := configPath("login.json")
	if err != nil {
		return err
	}
	_ = secret.Delete(keyringID)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
