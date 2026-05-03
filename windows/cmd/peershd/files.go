package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf16"

	v1 "github.com/peersh/peersh/core/protocol/peersh/v1"
	"github.com/peersh/peersh/core/transport"
	"github.com/peersh/peersh/core/wire"
	"github.com/peersh/peersh/windows/ptyhost"
)

// ptyRegistry tracks live PTY sessions for one QUIC connection so the
// Files-API request handler can look them up by the client-assigned
// pty_id. The lifetime of each entry matches the lifetime of the PTY
// stream that registered it; the per-stream goroutine calls Unregister
// in its defer.
type ptyRegistry struct {
	mu      sync.Mutex
	entries map[int64]*ptyhost.Session
}

func newPTYRegistry() *ptyRegistry {
	return &ptyRegistry{entries: make(map[int64]*ptyhost.Session)}
}

func (r *ptyRegistry) Register(id int64, s *ptyhost.Session) {
	if id == 0 {
		return
	}
	r.mu.Lock()
	r.entries[id] = s
	r.mu.Unlock()
}

func (r *ptyRegistry) Unregister(id int64) {
	if id == 0 {
		return
	}
	r.mu.Lock()
	delete(r.entries, id)
	r.mu.Unlock()
}

func (r *ptyRegistry) Get(id int64) (*ptyhost.Session, bool) {
	if id == 0 {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.entries[id]
	return s, ok
}

// MaxFileReadBytes is the per-request hard ceiling for ReadSessionFile.
// Clients can ask for less via max_bytes; asking for more is silently
// clamped down to this value.
const MaxFileReadBytes = 4 << 20 // 4 MiB

// serveFilesStream handles one client-initiated stream that begins with
// FilesRequest. The stream is a single request/response pair and is
// closed after the response is written. owner partitions any
// ptyhost.Manager lookups to this peer's PTYs.
func serveFilesStream(stream *transport.Stream, req *v1.FilesRequest, reg *ptyRegistry, mgr *ptyhost.Manager, owner ptyhost.Owner) {
	resp := &v1.FilesResponse{}
	switch kind := req.GetKind().(type) {
	case *v1.FilesRequest_GetSession:
		resp = handleGetSession(kind.GetSession, reg)
	case *v1.FilesRequest_ListFiles:
		resp = handleListFiles(kind.ListFiles, reg)
	case *v1.FilesRequest_ReadFile:
		resp = handleReadFile(kind.ReadFile, reg)
	case *v1.FilesRequest_ListPtys:
		resp = handleListPTYs(mgr, owner)
	case *v1.FilesRequest_KillPty:
		resp = handleKillPTY(kind.KillPty, mgr, owner)
	default:
		resp.Error = "files: unknown request kind"
	}
	_ = wire.Write(stream, resp)
}

func handleListPTYs(mgr *ptyhost.Manager, owner ptyhost.Owner) *v1.FilesResponse {
	listings := mgr.List(owner)
	out := &v1.ListPTYsResponse{}
	for _, l := range listings {
		out.Ptys = append(out.Ptys, &v1.PTYHandle{
			Handle:         string(l.Handle),
			Command:        l.Command,
			Attached:       l.Attached,
			Cwd:            l.CWD,
			LastSeenUnixMs: l.LastSeenMs,
		})
	}
	return &v1.FilesResponse{
		Payload: &v1.FilesResponse_ListPtys{ListPtys: out},
	}
}

func handleKillPTY(req *v1.KillPTYRequest, mgr *ptyhost.Manager, owner ptyhost.Owner) *v1.FilesResponse {
	mgr.Drop(owner, ptyhost.ManagedHandle(req.GetHandle()))
	return &v1.FilesResponse{
		Payload: &v1.FilesResponse_KillPty{KillPty: &v1.KillPTYResponse{Ok: true}},
	}
}

func handleGetSession(req *v1.GetSessionRequest, reg *ptyRegistry) *v1.FilesResponse {
	sess, ok := reg.Get(req.GetPtyId())
	if !ok {
		return &v1.FilesResponse{Error: "files: pty not found"}
	}
	return &v1.FilesResponse{
		Payload: &v1.FilesResponse_GetSession{
			GetSession: &v1.GetSessionResponse{Cwd: sess.CWD()},
		},
	}
}

func handleListFiles(req *v1.ListSessionFilesRequest, reg *ptyRegistry) *v1.FilesResponse {
	sess, ok := reg.Get(req.GetPtyId())
	if !ok {
		return &v1.FilesResponse{Error: "files: pty not found"}
	}
	cwd := sess.CWD()
	if cwd == "" {
		return &v1.FilesResponse{Error: "files: session has no observed cwd yet"}
	}
	resolved, err := resolveSessionPath(cwd, req.GetPath())
	if err != nil {
		return &v1.FilesResponse{Error: "files: " + err.Error()}
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return &v1.FilesResponse{Error: "files: " + err.Error()}
	}
	out := &v1.ListSessionFilesResponse{
		ResolvedPath: relativeToCWD(cwd, resolved),
	}
	for _, e := range entries {
		info, infoErr := e.Info()
		var size int64
		var modUnix int64
		if infoErr == nil {
			size = info.Size()
			modUnix = info.ModTime().UnixMilli()
		}
		entryPath := filepath.Join(req.GetPath(), e.Name())
		out.Entries = append(out.Entries, &v1.FileEntry{
			Name:           e.Name(),
			Path:           filepath.ToSlash(entryPath),
			IsDir:          e.IsDir(),
			Size:           size,
			ModifiedUnixMs: modUnix,
		})
	}
	return &v1.FilesResponse{
		Payload: &v1.FilesResponse_ListFiles{ListFiles: out},
	}
}

func handleReadFile(req *v1.ReadSessionFileRequest, reg *ptyRegistry) *v1.FilesResponse {
	sess, ok := reg.Get(req.GetPtyId())
	if !ok {
		return &v1.FilesResponse{Error: "files: pty not found"}
	}
	cwd := sess.CWD()
	if cwd == "" {
		return &v1.FilesResponse{Error: "files: session has no observed cwd yet"}
	}
	resolved, err := resolveSessionPath(cwd, req.GetPath())
	if err != nil {
		return &v1.FilesResponse{Error: "files: " + err.Error()}
	}
	stat, err := os.Stat(resolved)
	if err != nil {
		return &v1.FilesResponse{Error: "files: " + err.Error()}
	}
	if stat.IsDir() {
		return &v1.FilesResponse{Error: "files: path is a directory"}
	}

	max := req.GetMaxBytes()
	if max <= 0 || max > MaxFileReadBytes {
		max = MaxFileReadBytes
	}

	raw, err := os.ReadFile(resolved)
	if err != nil {
		return &v1.FilesResponse{Error: "files: " + err.Error()}
	}
	encoding, normalised := normaliseTextEncoding(raw)
	truncated := false
	if int64(len(normalised)) > max {
		normalised = normalised[:max]
		truncated = true
	}
	return &v1.FilesResponse{
		Payload: &v1.FilesResponse_ReadFile{
			ReadFile: &v1.ReadSessionFileResponse{
				Content:   normalised,
				Encoding:  encoding,
				Size:      stat.Size(),
				Truncated: truncated,
			},
		},
	}
}

// resolveSessionPath joins cwd and rel, then verifies the result is
// inside cwd. Returns the absolute, cleaned path.
func resolveSessionPath(cwd, rel string) (string, error) {
	cwd = filepath.Clean(cwd)
	rel = filepath.FromSlash(rel)
	rel = strings.TrimSpace(rel)
	if filepath.IsAbs(rel) {
		return "", errors.New("absolute paths are rejected; use cwd-relative paths")
	}
	full := filepath.Clean(filepath.Join(cwd, rel))
	// On Windows, filepath.Join + Clean does not resolve symlinks. We
	// accept that limitation: Tier 2 file browser is meant for the
	// session operator's own files, not as a hard sandbox. The cwd
	// containment check below catches obvious "../../" escape attempts.
	cwdAbs, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve cwd: %w", err)
	}
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	rel2, err := filepath.Rel(cwdAbs, fullAbs)
	if err != nil || strings.HasPrefix(rel2, "..") {
		return "", errors.New("path escapes the session cwd")
	}
	return fullAbs, nil
}

// relativeToCWD returns the cwd-relative form of `full`, with forward
// slashes for cross-platform display.
func relativeToCWD(cwd, full string) string {
	rel, err := filepath.Rel(cwd, full)
	if err != nil {
		return ""
	}
	if rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}

// normaliseTextEncoding sniffs the leading BOM and converts UTF-16-LE /
// UTF-16-BE to UTF-8 so the mobile viewer can render text uniformly.
// Files without a BOM are passed through as-is; the encoding label
// reflects what we observed (e.g. "utf-8" or "utf-8-bom").
func normaliseTextEncoding(raw []byte) (string, []byte) {
	if len(raw) >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF {
		return "utf-8-bom", raw[3:]
	}
	if len(raw) >= 2 && raw[0] == 0xFF && raw[1] == 0xFE {
		return "utf-16-le", utf16ToUTF8(raw[2:], false)
	}
	if len(raw) >= 2 && raw[0] == 0xFE && raw[1] == 0xFF {
		return "utf-16-be", utf16ToUTF8(raw[2:], true)
	}
	return "utf-8", raw
}

func utf16ToUTF8(raw []byte, bigEndian bool) []byte {
	if len(raw)%2 != 0 {
		raw = raw[:len(raw)-1]
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		hi, lo := raw[2*i], raw[2*i+1]
		if bigEndian {
			units[i] = uint16(hi)<<8 | uint16(lo)
		} else {
			units[i] = uint16(lo)<<8 | uint16(hi)
		}
	}
	runes := utf16.Decode(units)
	return []byte(string(runes))
}
