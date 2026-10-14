//go:build !linux

package signin

import "context"

func register(context.Context, string, func(string) error) (func(), string) {
	return func() {}, "Copy the sign-in code from Postman's success page back here to finish."
}
