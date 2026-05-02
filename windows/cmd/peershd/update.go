package main

// Self-update for peershd.exe. Triggered via:
//
//   peershd.exe update           # check + install if newer
//   peershd.exe update -check    # check only, exit 0 if up to date,
//                                # exit 1 if a newer release exists
//   peershd.exe update -force    # reinstall even if version matches
//
// Strategy:
//
//   1. GET https://api.github.com/repos/<embeddedUpdateRepo>/releases/latest
//      to read the latest published tag + assets.
//   2. Compare the tag (semver) against embeddedVersion.
//   3. Download the asset whose name matches "peershd-windows-amd64.exe"
//      (or fall back to "peershd.exe").
//   4. Verify the SHA-256 against the asset whose name ends in ".sha256"
//      (a sibling release artefact). Fail closed when missing.
//   5. Atomic swap: download to ".new", os.Rename current -> ".old",
//      os.Rename ".new" -> current, optionally delete ".old".
//   6. Re-exec with the same args (minus the update subcommand).
//
// Service mode:
//   When peershd is running as a Windows Service, the executable file
//   is locked. We detect this by attempting the rename — if it fails
//   with ERROR_SHARING_VIOLATION (raw errno 32 on Windows), print a
//   helpful message asking the operator to "peershd -stop" first.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// runUpdate parses subcommand flags and performs the install. Returns
// nil on success; a non-nil error halts main with exit 1.
func runUpdate(args []string) error {
	if embeddedUpdateRepo == "" {
		return errors.New("update: this binary was built without a release repo (set main.embeddedUpdateRepo via -ldflags)")
	}
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	checkOnly := fs.Bool("check", false, "report whether a newer release is available; do not install")
	force := fs.Bool("force", false, "install even when the latest release matches the running version")
	timeoutSec := fs.Int("timeout", 60, "HTTP timeout (seconds) for fetch operations")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client := &http.Client{Timeout: time.Duration(*timeoutSec) * time.Second}
	rel, err := fetchLatestRelease(client, embeddedUpdateRepo)
	if err != nil {
		return fmt.Errorf("fetch release: %w", err)
	}

	current := normalizeVersion(embeddedVersion)
	latest := normalizeVersion(rel.TagName)
	fmt.Printf("current: %s\nlatest : %s\n", current, latest)

	if !*force && current == latest {
		fmt.Println("Already up to date.")
		return nil
	}
	if *checkOnly {
		fmt.Printf("New version available: %s -> %s\n", current, latest)
		os.Exit(1)
	}

	asset, sumAsset, err := pickAssets(rel.Assets, "peershd")
	if err != nil {
		return err
	}

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("self path: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("resolve self path: %w", err)
	}

	// Download to <exe>.new, verify, then swap.
	newPath := exePath + ".new"
	oldPath := exePath + ".old"
	defer os.Remove(newPath) // safe even on success — Rename moves it

	fmt.Printf("Downloading %s ...\n", asset.BrowserDownloadURL)
	if err := downloadFile(client, asset.BrowserDownloadURL, newPath); err != nil {
		return fmt.Errorf("download: %w", err)
	}

	if sumAsset != nil {
		want, err := fetchExpectedSHA256(client, sumAsset.BrowserDownloadURL, asset.Name)
		if err != nil {
			return fmt.Errorf("fetch sha256: %w", err)
		}
		got, err := sha256File(newPath)
		if err != nil {
			return fmt.Errorf("hash downloaded file: %w", err)
		}
		if want != got {
			return fmt.Errorf("checksum mismatch: want=%s got=%s", want, got)
		}
		fmt.Println("Checksum verified.")
	} else {
		return fmt.Errorf("update: release %s does not include a .sha256 sibling for %q; refusing to install unverified binary", rel.TagName, asset.Name)
	}

	// Atomic swap.
	_ = os.Remove(oldPath) // tolerate stale leftover
	if err := os.Rename(exePath, oldPath); err != nil {
		if isFileInUse(err) {
			return fmt.Errorf("update: %s is locked by another process. If running as a Windows Service, stop it first: `peershd.exe -stop` (then re-run update). Original error: %w", filepath.Base(exePath), err)
		}
		return fmt.Errorf("rename %s -> %s: %w", exePath, oldPath, err)
	}
	if err := os.Rename(newPath, exePath); err != nil {
		// Best-effort recovery: put the old binary back.
		_ = os.Rename(oldPath, exePath)
		return fmt.Errorf("rename %s -> %s: %w", newPath, exePath, err)
	}
	_ = os.Remove(oldPath)
	fmt.Printf("Updated %s -> %s.\n", current, latest)
	return nil
}

// --- HTTP / GitHub helpers ------------------------------------------

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type release struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

func fetchLatestRelease(client *http.Client, repo string) (*release, error) {
	url := "https://api.github.com/repos/" + repo + "/releases/latest"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API %d: %s", resp.StatusCode, string(body))
	}
	var rel release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	if rel.TagName == "" {
		return nil, errors.New("release has no tag_name")
	}
	return &rel, nil
}

// pickAssets selects the platform-specific binary and (optionally) its
// .sha256 sibling. Naming convention: any asset name containing
// "<base>-<goos>-<goarch>" (e.g. peershd-windows-amd64.exe) wins; we
// fall back to bare "<base>.exe" / "<base>" for old releases.
func pickAssets(assets []releaseAsset, base string) (*releaseAsset, *releaseAsset, error) {
	want := fmt.Sprintf("%s-%s-%s", base, runtime.GOOS, runtime.GOARCH)
	exeSuffix := ""
	if runtime.GOOS == "windows" {
		exeSuffix = ".exe"
	}
	var bin *releaseAsset
	for i := range assets {
		a := &assets[i]
		switch a.Name {
		case want + exeSuffix, base + exeSuffix:
			bin = a
		}
		if bin == nil && strings.HasPrefix(a.Name, want) && !strings.HasSuffix(a.Name, ".sha256") {
			bin = a
		}
	}
	if bin == nil {
		return nil, nil, fmt.Errorf("no asset matched %s%s in release", want, exeSuffix)
	}
	var sum *releaseAsset
	for i := range assets {
		a := &assets[i]
		if a.Name == bin.Name+".sha256" || a.Name == "SHA256SUMS" {
			sum = a
			break
		}
	}
	return bin, sum, nil
}

func downloadFile(client *http.Client, url, dst string) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return err
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fetchExpectedSHA256 reads either a single-line `<hex>` file or a
// SHA256SUMS-style file (one `<hex>  <name>` line per artefact) and
// returns the hex digest matching binaryName.
func fetchExpectedSHA256(client *http.Client, url, binaryName string) (string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(body))
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 1 && len(fields[0]) == 64 {
			return strings.ToLower(fields[0]), nil
		}
		if len(fields) >= 2 && strings.HasSuffix(fields[len(fields)-1], binaryName) {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("no checksum entry for %s", binaryName)
}

// normalizeVersion strips a single leading 'v' (so "v1.2.3" and
// "1.2.3" compare equal). Anything past that is opaque.
func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "v") {
		v = v[1:]
	}
	return v
}

// isFileInUse reports whether err looks like Windows ERROR_SHARING_VIOLATION.
func isFileInUse(err error) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	if errors.Is(err, syscall.Errno(32)) {
		return true
	}
	// Fallback: parse text. Best-effort heuristic.
	return strings.Contains(strings.ToLower(err.Error()), "being used by another process")
}

// reExec re-runs the same binary after a successful update with the
// supplied args. Currently unused (the update subcommand exits cleanly
// and lets the operator decide when to restart) but kept here as a
// future hook for `peershd.exe update -restart`.
func reExec(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Start()
}

// versionCommand prints the embedded version + repo and exits.
func versionCommand() {
	repo := embeddedUpdateRepo
	if repo == "" {
		repo = "(none — local build)"
	}
	fmt.Printf("peersh %s\nrepo: %s\n", embeddedVersion, repo)
	if _, err := strconv.Atoi(embeddedVersion); err == nil {
		_ = err // silence unused
	}
}
