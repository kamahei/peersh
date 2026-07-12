//go:build !windows && !darwin

package pty

func spawn(_ string, _, _ []string, _, _ uint16) (PTY, error) {
	return nil, ErrUnsupported
}
