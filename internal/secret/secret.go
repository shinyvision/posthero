// Package secret keeps posthero's secrets (the Postman API key, saved
// logins, vault values) in the system keyring: the Secret Service (GNOME
// Keyring, KWallet) on Linux, the Keychain on macOS and the Credential
// Manager on Windows.
package secret

import (
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

// service is the name passwords are filed under.
const service = "posthero"

// ErrNotFound means the keyring has nothing stored under the id.
var ErrNotFound = keyring.ErrNotFound

// Get returns the secret stored under id.
func Get(id string) (string, error) {
	pw, err := keyring.Get(service, id)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return "", wrap(err)
	}
	return pw, err
}

// Set stores a secret under id.
func Set(id, password string) error {
	return wrap(keyring.Set(service, id, password))
}

// Delete removes the secret stored under id; a missing one is fine.
func Delete(id string) error {
	if err := keyring.Delete(service, id); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return wrap(err)
	}
	return nil
}

// Available reports why the keyring cannot be used, or nil when it can.
func Available() error {
	if _, err := keyring.Get(service, "posthero-probe"); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return wrap(err)
	}
	return nil
}

func wrap(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("the system keyring is not available (%w)", err)
}

// MockInit replaces the system keyring with an in-memory one, for tests.
func MockInit() { keyring.MockInit() }

// MockUnavailable replaces the system keyring with one that fails with
// err, for tests.
func MockUnavailable(err error) { keyring.MockInitWithError(err) }
