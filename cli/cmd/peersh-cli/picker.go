// Interactive PTY picker for the CLI.
//
// On `peersh-cli -pty` (without -reattach or -new) the CLI fetches the
// host's persisted PTY list via FilesRequest_ListPtys and prompts the
// user to attach to one, kill one, or create a new shell. The same
// list is shared across every device the operator has registered
// under their user_id (mobile app, other PCs, ...) — multi-attach
// means picking an already-attached entry just adds another live
// observer instead of stealing it.
//
// The prompt is line-based on purpose: it works in cmd.exe / Windows
// Terminal / mintty / ssh-into-Windows without any per-platform
// raw-mode tricks.

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	v1 "github.com/peersh/peersh/core/protocol/peersh/v1"
	"github.com/peersh/peersh/core/transport"
	"github.com/peersh/peersh/core/wire"
	fbpeershd "github.com/peersh/peersh/windows/firebase"
)

// pickFirebaseHost resolves the target device_id when the operator
// didn't pass -target in Firebase mode. With one registered host it
// auto-picks; with multiple it prompts the same way runPicker does.
// With zero, returns an actionable error pointing the user at
// `peershd -firebase-login`.
func pickFirebaseHost(ctx context.Context, src fbpeershd.TokenSource, opts firebaseOpts) (string, error) {
	hosts, err := listFirebaseHosts(ctx, src, opts.ProjectID, opts.RtdbRegion)
	if err != nil {
		return "", err
	}
	if len(hosts) == 0 {
		return "", errors.New("no Windows hosts registered for this Firebase user; run peershd with -firebase-login on the host first")
	}
	if len(hosts) == 1 {
		fmt.Fprintf(os.Stderr, "auto-selected host %s (%s)\n", hosts[0].DisplayName, hosts[0].DeviceID)
		return hosts[0].DeviceID, nil
	}
	fmt.Println("\nRegistered Windows hosts:")
	for i, h := range hosts {
		fmt.Printf("  %2d. %-24s  %s  %s\n",
			i+1, h.DisplayName, h.DeviceID, relativeTime(h.LastSeenUnixMs))
	}
	in := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("\nselect host (number) or [Q]uit > ")
		line, err := in.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.ToLower(line[:1]) == "q" {
			return "", errors.New("cancelled")
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 || n > len(hosts) {
			fmt.Println("  invalid selection")
			continue
		}
		return hosts[n-1].DeviceID, nil
	}
}

// pickerChoice is what runPicker returned. Exactly one of Handle /
// NewPTY is set on a non-quit return; both empty means the user chose
// to quit.
type pickerChoice struct {
	Handle string
	NewPTY bool
}

// runPicker is the interactive list/select loop.
func runPicker(ctx context.Context, conn *transport.Conn) (pickerChoice, error) {
	in := bufio.NewReader(os.Stdin)
	for {
		ptys, err := listPTYs(ctx, conn)
		if err != nil {
			return pickerChoice{}, fmt.Errorf("list ptys: %w", err)
		}
		renderPTYList(ptys)
		fmt.Print("\nattach: number  |  [N]ew  |  [X<n>] kill  |  [R]efresh  |  [Q]uit > ")
		line, err := in.ReadString('\n')
		if err != nil {
			return pickerChoice{}, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		head := strings.ToLower(line[:1])
		switch head {
		case "n":
			return pickerChoice{NewPTY: true}, nil
		case "q":
			return pickerChoice{}, nil
		case "r":
			continue
		case "x":
			rest := strings.TrimSpace(line[1:])
			n, err := strconv.Atoi(rest)
			if err != nil || n < 1 || n > len(ptys) {
				fmt.Println("  invalid selection")
				continue
			}
			if err := killPTY(ctx, conn, ptys[n-1].GetHandle()); err != nil {
				fmt.Println("  kill:", err)
			}
			continue
		}
		// numeric selection
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 || n > len(ptys) {
			fmt.Println("  invalid selection")
			continue
		}
		return pickerChoice{Handle: ptys[n-1].GetHandle()}, nil
	}
}

func renderPTYList(ptys []*v1.PTYHandle) {
	if len(ptys) == 0 {
		fmt.Println("\nNo persisted PTY sessions on host.")
		return
	}
	fmt.Println("\nPersisted PTY sessions on host:")
	for i, p := range ptys {
		var clients string
		switch p.GetAttachedCount() {
		case 0:
			clients = "detached"
		case 1:
			clients = "1 client"
		default:
			clients = fmt.Sprintf("%d clients", p.GetAttachedCount())
		}
		h := p.GetHandle()
		if len(h) > 8 {
			h = h[:8]
		}
		cwd := p.GetCwd()
		if cwd == "" {
			cwd = "(no cwd yet)"
		}
		seen := relativeTime(p.GetLastSeenUnixMs())
		fmt.Printf("  %2d. %-8s  %-8s  %-10s  %-30s  %s\n",
			i+1, h, p.GetCommand(), clients, cwd, seen)
	}
}

func relativeTime(unixMs int64) string {
	if unixMs == 0 {
		return ""
	}
	d := time.Since(time.UnixMilli(unixMs))
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// listPTYs opens a fresh stream, sends a FilesRequest_ListPtys, and
// returns the response. The host scopes the list by Owner so the CLI
// only ever sees its own user's PTYs.
func listPTYs(ctx context.Context, conn *transport.Conn) ([]*v1.PTYHandle, error) {
	s, err := conn.OpenStream(ctx)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	if err := wire.Write(s, &v1.StreamRequest{
		Kind: &v1.StreamRequest_Files{Files: &v1.FilesRequest{
			Kind: &v1.FilesRequest_ListPtys{ListPtys: &v1.ListPTYsRequest{}},
		}},
	}); err != nil {
		return nil, err
	}
	r := wire.NewReader(s)
	resp := &v1.FilesResponse{}
	if err := wire.Read(r, resp); err != nil {
		return nil, err
	}
	if resp.GetError() != "" {
		return nil, errors.New(resp.GetError())
	}
	return resp.GetListPtys().GetPtys(), nil
}

func killPTY(ctx context.Context, conn *transport.Conn, handle string) error {
	s, err := conn.OpenStream(ctx)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := wire.Write(s, &v1.StreamRequest{
		Kind: &v1.StreamRequest_Files{Files: &v1.FilesRequest{
			Kind: &v1.FilesRequest_KillPty{KillPty: &v1.KillPTYRequest{Handle: handle}},
		}},
	}); err != nil {
		return err
	}
	r := wire.NewReader(s)
	resp := &v1.FilesResponse{}
	if err := wire.Read(r, resp); err != nil {
		return err
	}
	if resp.GetError() != "" {
		return errors.New(resp.GetError())
	}
	return nil
}
