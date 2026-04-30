import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../services/peersh_session.dart';

/// Built-in simple text viewer. Pulls the file content over the active
/// session via Get-Content, then offers in-page search with match
/// navigation, copy-all, and meta in the AppBar.
class TextViewerScreen extends ConsumerStatefulWidget {
  const TextViewerScreen({
    super.key,
    required this.session,
    required this.path,
  });

  final PeershSession session;
  final String path;

  @override
  ConsumerState<TextViewerScreen> createState() => _TextViewerScreenState();
}

class _TextViewerScreenState extends ConsumerState<TextViewerScreen> {
  final _searchCtrl = TextEditingController();
  String? _content;
  String? _error;
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
      final raw = await widget.session.readFile(widget.path);
      if (!mounted) return;
      if (raw.startsWith('ERROR: ')) {
        setState(() => _error = raw.substring(7));
      } else {
        setState(() => _content = raw);
        _recomputeMatches();
      }
    } catch (e) {
      if (!mounted) return;
      setState(() => _error = '$e');
    }
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

  String _meta() {
    final c = _content;
    if (c == null) return '';
    final bytes = c.codeUnits.length;
    final size = bytes < 1024
        ? '$bytes B'
        : bytes < 1024 * 1024
            ? '${(bytes / 1024).toStringAsFixed(1)} KiB'
            : '${(bytes / (1024 * 1024)).toStringAsFixed(1)} MiB';
    return 'UTF-8 · $size';
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: Text(widget.path, overflow: TextOverflow.ellipsis),
        actions: [
          IconButton(
            tooltip: 'Copy all',
            onPressed: _content == null ? null : _copyAll,
            icon: const Icon(Icons.copy_all_outlined),
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
                      child: SingleChildScrollView(
                        padding: const EdgeInsets.all(12),
                        child: SelectableText(
                          _content!,
                          style: const TextStyle(
                            fontFamily: 'monospace',
                            fontSize: 13,
                          ),
                        ),
                      ),
                    ),
                  ],
                ),
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
