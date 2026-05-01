//go:build windows

package ptyhost_test

import "os/exec"

// execLookPath wraps exec.LookPath so manager_test.go's lookExe doesn't
// need to import os/exec at the top — keeps the imports minimal there.
func execLookPath(name string) (string, error) { return exec.LookPath(name) }
