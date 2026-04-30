package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const logonTaskName = "peershd"

// runLogonTask handles -install-logon-task and -uninstall-logon-task.
// Returns handled=true when one of those flags was supplied.
//
// Implementation: defers to Windows' built-in `schtasks.exe`. We
// register peershd as a Windows Scheduled Task with trigger "AT LOGON"
// for the chosen user. The task action is the absolute path to the
// peershd executable plus the same args it was invoked with (minus the
// service / logon-task control flags).
func runLogonTask(argv []string) (handled bool, err error) {
	fs := flag.NewFlagSet("logon-task", flag.ContinueOnError)
	install := fs.Bool("install-logon-task", false, "register peershd as a Windows Scheduled Task that runs at user logon")
	uninstall := fs.Bool("uninstall-logon-task", false, "remove the peershd logon-time Scheduled Task")
	user := fs.String("logon-task-user", "", "Windows account to run the task as (default: current user; e.g. 'DOMAIN\\\\Alice' or '.\\Alice')")
	fs.SetOutput(devNull{})
	_ = fs.Parse(argv[1:])

	if !*install && !*uninstall {
		return false, nil
	}

	if *install {
		return true, installLogonTask(argv, *user)
	}
	return true, uninstallLogonTask()
}

func installLogonTask(argv []string, user string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return err
	}
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("peershd binary not found at %s: %w", exe, err)
	}

	// Build the action's argument list — strip control flags so the
	// scheduled task does not loop into install/uninstall.
	taskArgs := stripLogonTaskFlags(stripServiceFlags(argv[1:]))
	tr := quoteForSchtasks(exe)
	if len(taskArgs) > 0 {
		tr += " " + strings.Join(quoteAll(taskArgs), " ")
	}

	cmdArgs := []string{
		"/Create",
		"/TN", logonTaskName,
		"/SC", "ONLOGON",
		"/RL", "HIGHEST",
		"/F", // overwrite if exists
		"/TR", tr,
	}
	if user != "" {
		cmdArgs = append(cmdArgs, "/RU", user)
	}

	out, err := exec.Command("schtasks.exe", cmdArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks /Create failed: %w; output: %s", err, string(out))
	}
	fmt.Printf("Registered peersh logon task. action=%q\n", tr)
	return nil
}

func uninstallLogonTask() error {
	out, err := exec.Command("schtasks.exe", "/Delete", "/TN", logonTaskName, "/F").CombinedOutput()
	if err != nil {
		// Treat "task not found" as success.
		if strings.Contains(string(out), "cannot find") || strings.Contains(string(out), "見つかりません") {
			fmt.Println("peersh logon task was not registered.")
			return nil
		}
		return fmt.Errorf("schtasks /Delete failed: %w; output: %s", err, string(out))
	}
	fmt.Println("Removed peersh logon task.")
	return nil
}

// stripLogonTaskFlags removes the two install/uninstall flags from the
// arg list so the scheduled task action does not recursively try to
// re-install itself.
func stripLogonTaskFlags(args []string) []string {
	out := make([]string, 0, len(args))
	skip := false
	for i, a := range args {
		if skip {
			skip = false
			continue
		}
		switch a {
		case "-install-logon-task", "--install-logon-task",
			"-uninstall-logon-task", "--uninstall-logon-task":
			continue
		case "-logon-task-user", "--logon-task-user":
			// Skip both this flag and its value if present.
			if i+1 < len(args) {
				skip = true
			}
			continue
		}
		// Handle = form (-flag=value)
		if strings.HasPrefix(a, "-logon-task-user=") || strings.HasPrefix(a, "--logon-task-user=") {
			continue
		}
		out = append(out, a)
	}
	if len(out) == 0 && len(args) == 0 {
		return nil
	}
	if errors.Is(nil, nil) {
		// Defensive no-op to keep the function tidy across edits.
	}
	return out
}

// quoteForSchtasks wraps a string in double quotes if it contains a
// space. schtasks treats /TR as a single token so the whole string —
// command + args — must be quoted at this layer.
func quoteForSchtasks(s string) string {
	if !strings.ContainsAny(s, " \t") {
		return s
	}
	// Inside an outer "..." that schtasks will read, escape inner
	// double-quotes as backslash-double-quote.
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func quoteAll(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		out = append(out, quoteForSchtasks(a))
	}
	return out
}
