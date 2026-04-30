package auth

import "fmt"

// WrongKindError is returned by a Provider when it is handed credentials of an
// unexpected kind. Wrapping this in errors.As lets callers distinguish
// configuration mistakes from real authentication failures.
type WrongKindError struct {
	Got  string
	Want string
}

func (e *WrongKindError) Error() string {
	return fmt.Sprintf("auth: wrong credentials kind: got %q, want %q", e.Got, e.Want)
}
