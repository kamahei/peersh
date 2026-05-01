/// One persisted PTY the host is willing to let the client reattach to.
class PtyHandleInfo {
  const PtyHandleInfo({
    required this.handle,
    required this.command,
    required this.attached,
    required this.cwd,
    required this.lastSeenUnixMs,
  });

  /// Server-assigned reattach handle.
  final String handle;
  /// What the PTY is running ("auto" / "pwsh" / "claude" / ...).
  final String command;
  /// True when another stream is currently bound. Trying to reattach
  /// to an already-attached handle is rejected by the server.
  final bool attached;
  /// Last-observed cwd (or empty if no prompt has rendered yet).
  final String cwd;
  /// Time of the most recent attach/detach transition.
  final int lastSeenUnixMs;

  static PtyHandleInfo fromMap(Map<dynamic, dynamic> m) => PtyHandleInfo(
        handle: (m['handle'] as String?) ?? '',
        command: (m['command'] as String?) ?? '',
        attached: (m['attached'] as bool?) ?? false,
        cwd: (m['cwd'] as String?) ?? '',
        lastSeenUnixMs: (m['lastSeenUnixMs'] as num?)?.toInt() ?? 0,
      );
}

/// Cwd-relative file entry returned by `bridge.listSessionFiles`.
class PtyFileEntry {
  const PtyFileEntry({
    required this.name,
    required this.path,
    required this.isDir,
    required this.size,
    required this.modifiedUnixMs,
  });

  final String name;
  final String path;
  final bool isDir;
  final int size;
  final int modifiedUnixMs;

  static PtyFileEntry fromMap(Map<dynamic, dynamic> m) => PtyFileEntry(
        name: (m['name'] as String?) ?? '',
        path: (m['path'] as String?) ?? '',
        isDir: (m['isDir'] as bool?) ?? false,
        size: (m['size'] as num?)?.toInt() ?? 0,
        modifiedUnixMs: (m['modifiedUnixMs'] as num?)?.toInt() ?? 0,
      );
}

/// File content + metadata returned by `bridge.readSessionFile`.
class PtyFileContent {
  const PtyFileContent({
    required this.content,
    required this.encoding,
    required this.size,
    required this.truncated,
    this.error = '',
  });

  /// UTF-8 bytes (peershd transcodes UTF-16 source files at the host).
  final List<int> content;

  /// Encoding label as observed at the host: "utf-8", "utf-8-bom",
  /// "utf-16-le", or "utf-16-be".
  final String encoding;

  /// File size on disk, in bytes.
  final int size;

  /// True when [content] was clipped to the request's max_bytes.
  final bool truncated;

  /// Non-empty when the host refused the request (path escaped cwd,
  /// pty closed, file missing, ...).
  final String error;

  bool get isError => error.isNotEmpty;

  static PtyFileContent fromMap(Map<dynamic, dynamic> m) {
    final raw = m['content'];
    final List<int> bytes;
    if (raw == null) {
      bytes = const <int>[];
    } else if (raw is List<int>) {
      bytes = raw;
    } else if (raw is List) {
      bytes = List<int>.from(raw.cast<int>());
    } else {
      bytes = const <int>[];
    }
    return PtyFileContent(
      content: bytes,
      encoding: (m['encoding'] as String?) ?? '',
      size: (m['size'] as num?)?.toInt() ?? 0,
      truncated: (m['truncated'] as bool?) ?? false,
      error: (m['error'] as String?) ?? '',
    );
  }
}
