// Phase 8 Tier 2 — read-only text viewer.
//
// Two construction modes:
//
//   - default: takes a PeershSession + an absolute path (legacy entry
//     point from the terminal's "View remote file" dialog). Uses the
//     one-shot Get-Content Exec via PeershSession.readFile. Encoding is
//     always reported as UTF-8 (Get-Content -Encoding UTF8) and size is
//     computed from the captured content.
//
//   - forSession: takes a ptyId + cwd-relative path (entry point from
//     FileBrowserScreen). Uses bridge.readSessionFile, which returns
//     the host-side encoding label, on-disk size, and whether the
//     content was clipped by the host's max_bytes cap.
//
// Both modes share the same UI: search field with match navigation,
// syntax-highlight toggle, copy-all action, encoding+size meta line.

import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_highlighting/themes/github-dark.dart';
import 'package:flutter_highlighting/themes/github.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../files/syntax_highlighting.dart';
import '../services/peersh_session.dart';

class TextViewerScreen extends ConsumerStatefulWidget {
  const TextViewerScreen({
    super.key,
    required this.session,
    required this.path,
    this.title,
  })  : ptyId = null,
        sessionRelativePath = null;

  /// Session-scoped entry point: paths resolve against the host shell's
  /// last-observed cwd (via OSC 9;9). Used by FileBrowserScreen.
  const TextViewerScreen.forSession({
    super.key,
    required int this.ptyId,
    required String this.sessionRelativePath,
    required this.title,
  })  : session = null,
        path = '';

  final PeershSession? session;
  final int? ptyId;
  final String? sessionRelativePath;
  final String path;
  final String? title;

  bool get isSessionScoped => ptyId != null;

  @override
  ConsumerState<TextViewerScreen> createState() => _TextViewerScreenState();
}

class _TextViewerScreenState extends ConsumerState<TextViewerScreen> {
  final _searchCtrl = TextEditingController();
  String? _content;
  String? _error;
  String _encoding = 'utf-8';
  int _sizeOnDisk = 0;
  bool _truncated = false;
  bool _syntaxHighlight = true;
  List<int> _matches = const <int>[];
  int _currentMatch = -1;

  @override
  void initState() {
    super.initState();
    _load();
    _searchCtrl.addListener(_recomputeMatches);
  }

  @override
  void dispose() {
    _searchCtrl.removeListener(_recomputeMatches);
    _searchCtrl.dispose();
    super.dispose();
  }

  Future<void> _load() async {
    try {
      if (widget.isSessionScoped) {
        final fc = await ref.read(bridgeProvider).readSessionFile(
              ptyId: widget.ptyId!,
              path: widget.sessionRelativePath!,
            );
        if (!mounted) return;
        if (fc.isError) {
          setState(() => _error = fc.error);
          return;
        }
        setState(() {
          _content = utf8.decode(fc.content, allowMalformed: true);
          _encoding = fc.encoding;
          _sizeOnDisk = fc.size;
          _truncated = fc.truncated;
        });
      } else {
        final raw = await widget.session!.readFile(widget.path);
        if (!mounted) return;
        if (raw.startsWith('ERROR: ')) {
          setState(() => _error = raw.substring(7));
          return;
        }
        setState(() {
          _content = raw;
          _encoding = 'utf-8';
          _sizeOnDisk = raw.codeUnits.length;
          _truncated = false;
        });
      }
      _recomputeMatches();
    } catch (e) {
      if (!mounted) return;
      setState(() => _error = '$e');
    }
  }

  String get _displayPath {
    if (widget.isSessionScoped) return widget.sessionRelativePath ?? '';
    return widget.path;
  }

  void _recomputeMatches() {
    final content = _content;
    final query = _searchCtrl.text;
    if (content == null || query.isEmpty) {
      setState(() {
        _matches = const <int>[];
        _currentMatch = -1;
      });
      return;
    }
    final lc = content.toLowerCase();
    final lq = query.toLowerCase();
    final matches = <int>[];
    var pos = 0;
    while (true) {
      final idx = lc.indexOf(lq, pos);
      if (idx < 0) break;
      matches.add(idx);
      pos = idx + lq.length;
    }
    setState(() {
      _matches = matches;
      _currentMatch = matches.isEmpty ? -1 : 0;
    });
  }

  void _moveMatch(bool forward) {
    if (_matches.isEmpty) return;
    setState(() {
      if (forward) {
        _currentMatch = (_currentMatch + 1) % _matches.length;
      } else {
        _currentMatch = (_currentMatch - 1 + _matches.length) % _matches.length;
      }
    });
  }

  Future<void> _copyAll() async {
    final c = _content;
    if (c == null) return;
    await Clipboard.setData(ClipboardData(text: c));
    if (!mounted) return;
    ScaffoldMessenger.of(context).showSnackBar(
      const SnackBar(content: Text('Copied to clipboard')),
    );
  }

  String _formatSize(int size) {
    if (size < 1024) return '$size B';
    if (size < 1024 * 1024) return '${(size / 1024).toStringAsFixed(1)} KiB';
    return '${(size / (1024 * 1024)).toStringAsFixed(1)} MiB';
  }

  String _meta() {
    if (_content == null) return '';
    final tag = _truncated ? ' · truncated' : '';
    return '$_encoding · ${_formatSize(_sizeOnDisk)}$tag';
  }

  @override
  Widget build(BuildContext context) {
    final title = widget.title ?? _displayPath;
    return Scaffold(
      appBar: AppBar(
        title: Text(title, overflow: TextOverflow.ellipsis),
        actions: [
          IconButton(
            tooltip: 'Copy all',
            onPressed: _content == null ? null : _copyAll,
            icon: const Icon(Icons.copy_all_outlined),
          ),
          IconButton(
            tooltip: _syntaxHighlight
                ? 'Disable syntax highlight'
                : 'Enable syntax highlight',
            onPressed: () =>
                setState(() => _syntaxHighlight = !_syntaxHighlight),
            icon: Icon(
              _syntaxHighlight
                  ? Icons.palette_outlined
                  : Icons.format_color_reset_outlined,
            ),
          ),
        ],
        bottom: PreferredSize(
          preferredSize: const Size.fromHeight(20),
          child: Container(
            alignment: Alignment.centerLeft,
            padding: const EdgeInsets.symmetric(horizontal: 16),
            child: Text(_meta(), style: const TextStyle(fontSize: 12)),
          ),
        ),
      ),
      body: _error != null
          ? _ErrorState(message: _error!)
          : _content == null
              ? const Center(child: CircularProgressIndicator())
              : Column(
                  children: [
                    _SearchBar(
                      controller: _searchCtrl,
                      total: _matches.length,
                      current: _currentMatch,
                      onPrev: () => _moveMatch(false),
                      onNext: () => _moveMatch(true),
                    ),
                    Expanded(
                      child: _TextContent(
                        content: _content!,
                        path: _displayPath,
                        syntaxHighlight: _syntaxHighlight && _searchCtrl.text.isEmpty,
                      ),
                    ),
                  ],
                ),
    );
  }
}

class _TextContent extends StatelessWidget {
  const _TextContent({
    required this.content,
    required this.path,
    required this.syntaxHighlight,
  });

  final String content;
  final String path;
  final bool syntaxHighlight;

  @override
  Widget build(BuildContext context) {
    final isDark = Theme.of(context).brightness == Brightness.dark;
    final theme = isDark ? githubDarkTheme : githubTheme;
    final base = TextStyle(
      fontFamily: 'monospace',
      fontSize: 13,
      color: Theme.of(context).colorScheme.onSurface,
    );
    final language =
        syntaxHighlight ? highlightLanguageForPath(path) : null;
    final inner = (language == null)
        ? SelectableText(content, style: base)
        : SelectableText.rich(
            TextSpan(
              children: syntaxHighlightSpans(
                content: content,
                language: language,
                theme: theme,
                baseStyle: base,
              ),
              style: base,
            ),
          );
    return SingleChildScrollView(
      padding: const EdgeInsets.all(12),
      child: inner,
    );
  }
}

class _ErrorState extends StatelessWidget {
  const _ErrorState({required this.message});
  final String message;

  @override
  Widget build(BuildContext context) {
    return Center(
      child: Padding(
        padding: const EdgeInsets.all(24),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            const Icon(Icons.error_outline, size: 48),
            const SizedBox(height: 12),
            SelectableText(
              message,
              style: const TextStyle(fontFamily: 'monospace'),
              textAlign: TextAlign.center,
            ),
          ],
        ),
      ),
    );
  }
}

class _SearchBar extends StatelessWidget {
  const _SearchBar({
    required this.controller,
    required this.total,
    required this.current,
    required this.onPrev,
    required this.onNext,
  });
  final TextEditingController controller;
  final int total;
  final int current;
  final VoidCallback onPrev;
  final VoidCallback onNext;

  @override
  Widget build(BuildContext context) {
    return Material(
      color: Theme.of(context).colorScheme.surfaceContainer,
      child: Padding(
        padding: const EdgeInsets.fromLTRB(12, 8, 8, 8),
        child: Row(
          children: [
            Expanded(
              child: TextField(
                controller: controller,
                decoration: const InputDecoration(
                  isDense: true,
                  prefixIcon: Icon(Icons.search),
                  labelText: 'Search',
                  border: OutlineInputBorder(),
                ),
              ),
            ),
            const SizedBox(width: 8),
            Text(total == 0 ? '0/0' : '${current + 1}/$total'),
            IconButton(
              tooltip: 'Previous match',
              onPressed: total == 0 ? null : onPrev,
              icon: const Icon(Icons.keyboard_arrow_up),
            ),
            IconButton(
              tooltip: 'Next match',
              onPressed: total == 0 ? null : onNext,
              icon: const Icon(Icons.keyboard_arrow_down),
            ),
          ],
        ),
      ),
    );
  }
}
