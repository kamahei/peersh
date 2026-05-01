// Phase 8 Tier 2 — session-scoped file browser.
//
// Reachable from TerminalScreen's AppBar. Lists files relative to the
// host shell's last-observed cwd (via OSC 9;9 prompt instrumentation),
// which in practice means "wherever the user last `cd`d to in the
// terminal." Tapping a directory navigates into it; tapping a file
// opens TextViewerScreen against the session.
//
// Differences vs the peersh equivalent:
//   - peersh has no operator-configured "file roots" concept, so this
//     screen is session-scoped only. No bookmarks, no root dropdown.
//   - The view is bound to the live PTY (the user can multitask between
//     the terminal and the file browser; the cwd updates as the user
//     `cd`s in the live shell).

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../models/pty_file.dart';
import '../services/peersh_session.dart';
import 'text_viewer_screen.dart';

class FileBrowserScreen extends ConsumerStatefulWidget {
  const FileBrowserScreen({
    super.key,
    required this.ptyId,
    this.title,
  });

  final int ptyId;
  final String? title;

  @override
  ConsumerState<FileBrowserScreen> createState() =>
      _FileBrowserScreenState();
}

class _FileBrowserScreenState extends ConsumerState<FileBrowserScreen> {
  String _cwd = '';
  String _path = '';
  List<PtyFileEntry> _entries = const [];
  bool _loading = true;
  String? _error;

  @override
  void initState() {
    super.initState();
    _refresh();
  }

  Future<void> _refresh() async {
    setState(() {
      _loading = true;
      _error = null;
    });
    final bridge = ref.read(bridgeProvider);
    try {
      final cwd = await bridge.getCwd(ptyId: widget.ptyId);
      final list = await bridge.listSessionFiles(
        ptyId: widget.ptyId,
        path: _path,
      );
      if (!mounted) return;
      setState(() {
        _cwd = cwd;
        _entries = list..sort(_sortEntries);
        _loading = false;
      });
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _error = '$e';
        _loading = false;
      });
    }
  }

  static int _sortEntries(PtyFileEntry a, PtyFileEntry b) {
    if (a.isDir != b.isDir) return a.isDir ? -1 : 1;
    return a.name.toLowerCase().compareTo(b.name.toLowerCase());
  }

  Future<void> _openPath(String path) async {
    setState(() {
      _path = path;
      _entries = const [];
    });
    await _refresh();
  }

  Future<void> _goParent() async {
    if (_path.isEmpty) return;
    final parts = _path.split('/')..removeWhere((p) => p.isEmpty);
    if (parts.isNotEmpty) parts.removeLast();
    await _openPath(parts.join('/'));
  }

  Future<void> _openEntry(PtyFileEntry entry) async {
    if (entry.isDir) {
      await _openPath(entry.path);
      return;
    }
    if (!mounted) return;
    await Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => TextViewerScreen.forSession(
          ptyId: widget.ptyId,
          sessionRelativePath: entry.path,
          title: entry.name,
        ),
      ),
    );
  }

  Future<void> _showPathDialog() async {
    final controller = TextEditingController(text: _path);
    final next = await showDialog<String>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Open path'),
        content: TextField(
          controller: controller,
          autofocus: true,
          decoration: const InputDecoration(
            labelText: 'Path (cwd-relative)',
            hintText: 'src/main',
          ),
          onSubmitted: (v) => Navigator.pop(ctx, v),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx),
            child: const Text('Cancel'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(ctx, controller.text),
            child: const Text('Open'),
          ),
        ],
      ),
    );
    controller.dispose();
    if (next == null || !mounted) return;
    await _openPath(next.trim());
  }

  @override
  Widget build(BuildContext context) {
    final title = widget.title ?? 'Files';
    return Scaffold(
      appBar: AppBar(
        title: Text(title),
        actions: [
          IconButton(
            tooltip: 'Refresh',
            onPressed: _refresh,
            icon: const Icon(Icons.refresh),
          ),
        ],
      ),
      body: _buildBody(context),
    );
  }

  Widget _buildBody(BuildContext context) {
    if (_loading && _entries.isEmpty && _error == null) {
      return const Center(child: CircularProgressIndicator());
    }
    final cwdLabel = _cwd.isEmpty ? '(no cwd observed yet)' : _cwd;
    return Column(
      children: [
        Material(
          color: Theme.of(context).colorScheme.surfaceContainer,
          child: Padding(
            padding: const EdgeInsets.fromLTRB(12, 8, 8, 8),
            child: Row(
              children: [
                Expanded(
                  child: Text(
                    cwdLabel,
                    overflow: TextOverflow.ellipsis,
                    style: Theme.of(context).textTheme.titleSmall,
                  ),
                ),
                IconButton(
                  icon: const Icon(Icons.drive_folder_upload_outlined),
                  tooltip: 'Parent directory',
                  onPressed: _path.isEmpty ? null : _goParent,
                ),
                IconButton(
                  icon: const Icon(Icons.edit_location_alt_outlined),
                  tooltip: 'Open path',
                  onPressed: _showPathDialog,
                ),
              ],
            ),
          ),
        ),
        _Breadcrumbs(
          rootLabel: cwdLabel,
          path: _path,
          onOpen: _openPath,
        ),
        if (_error != null)
          Padding(
            padding: const EdgeInsets.all(12),
            child: Text(
              _error!,
              style: TextStyle(color: Theme.of(context).colorScheme.error),
            ),
          ),
        Expanded(
          child: Stack(
            children: [
              if (_entries.isEmpty && !_loading && _error == null)
                Center(
                  child: Text(
                    _path.isEmpty ? 'Empty directory.' : 'No entries at $_path.',
                    style: Theme.of(context).textTheme.bodyMedium,
                  ),
                )
              else
                ListView.separated(
                  itemCount: _entries.length,
                  separatorBuilder: (_, __) => const Divider(height: 1),
                  itemBuilder: (_, i) => _entryTile(context, _entries[i]),
                ),
              if (_loading)
                const Align(
                  alignment: Alignment.topCenter,
                  child: LinearProgressIndicator(),
                ),
            ],
          ),
        ),
      ],
    );
  }

  Widget _entryTile(BuildContext context, PtyFileEntry entry) {
    return ListTile(
      leading: Icon(
        entry.isDir ? Icons.folder_outlined : Icons.description_outlined,
      ),
      title: Text(entry.name),
      subtitle: Text(_subtitle(context, entry)),
      onTap: () => _openEntry(entry),
    );
  }

  String _subtitle(BuildContext context, PtyFileEntry entry) {
    final modText = entry.modifiedUnixMs > 0
        ? MaterialLocalizations.of(context).formatShortDate(
            DateTime.fromMillisecondsSinceEpoch(entry.modifiedUnixMs).toLocal(),
          )
        : '';
    if (entry.isDir) return modText.isEmpty ? 'directory' : 'dir · $modText';
    return modText.isEmpty
        ? formatFileSize(entry.size)
        : '${formatFileSize(entry.size)} · $modText';
  }
}

/// A horizontally-scrolling row of action chips: cwd label, then each
/// segment of the current relative path. Tapping a chip jumps there.
class _Breadcrumbs extends StatelessWidget {
  const _Breadcrumbs({
    required this.rootLabel,
    required this.path,
    required this.onOpen,
  });

  final String rootLabel;
  final String path;
  final ValueChanged<String> onOpen;

  @override
  Widget build(BuildContext context) {
    final segments = path.split('/')..removeWhere((s) => s.isEmpty);
    return SizedBox(
      height: 44,
      child: ListView(
        scrollDirection: Axis.horizontal,
        padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 6),
        children: [
          ActionChip(
            label: Text(rootLabel),
            onPressed: () => onOpen(''),
          ),
          for (var i = 0; i < segments.length; i++) ...[
            const Padding(
              padding: EdgeInsets.symmetric(horizontal: 4),
              child: Center(child: Text('/')),
            ),
            ActionChip(
              label: Text(segments[i]),
              onPressed: () => onOpen(segments.take(i + 1).join('/')),
            ),
          ],
        ],
      ),
    );
  }
}

/// Human-readable byte size: B, KiB, MiB.
String formatFileSize(int size) {
  if (size < 1024) return '$size B';
  if (size < 1024 * 1024) return '${(size / 1024).toStringAsFixed(1)} KiB';
  return '${(size / (1024 * 1024)).toStringAsFixed(1)} MiB';
}
