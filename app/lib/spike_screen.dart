import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'bridge.dart';

final _bridgeProvider = Provider<PeershBridge>((ref) => PeershBridge());

class SpikeScreen extends ConsumerStatefulWidget {
  const SpikeScreen({super.key});

  @override
  ConsumerState<SpikeScreen> createState() => _SpikeScreenState();
}

class _SpikeScreenState extends ConsumerState<SpikeScreen> {
  final _addrCtrl = TextEditingController(text: '127.0.0.1:7777');
  final _cmdCtrl = TextEditingController(text: 'Get-Date');

  String _output = '';
  bool _running = false;
  String? _version;

  @override
  void initState() {
    super.initState();
    _loadVersion();
  }

  Future<void> _loadVersion() async {
    try {
      final v = await ref.read(_bridgeProvider).version();
      if (!mounted) return;
      setState(() => _version = v);
    } catch (_) {}
  }

  Future<void> _run() async {
    setState(() {
      _running = true;
      _output = '';
    });
    try {
      final out = await ref.read(_bridgeProvider).echo(
            addr: _addrCtrl.text.trim(),
            command: _cmdCtrl.text,
          );
      if (!mounted) return;
      setState(() => _output = out);
    } catch (e) {
      if (!mounted) return;
      setState(() => _output = 'BRIDGE ERROR: $e');
    } finally {
      if (mounted) setState(() => _running = false);
    }
  }

  @override
  void dispose() {
    _addrCtrl.dispose();
    _cmdCtrl.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: const Text('peersh — gomobile spike'),
        bottom: PreferredSize(
          preferredSize: const Size.fromHeight(20),
          child: Container(
            alignment: Alignment.centerLeft,
            padding: const EdgeInsets.symmetric(horizontal: 16),
            child: Text(
              _version == null ? 'mobile-core: …' : 'mobile-core: $_version',
              style: const TextStyle(fontSize: 12),
            ),
          ),
        ),
      ),
      body: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            TextField(
              controller: _addrCtrl,
              decoration: const InputDecoration(
                labelText: 'peershd address (host:port)',
                hintText: '192.168.1.5:7777',
              ),
            ),
            const SizedBox(height: 12),
            TextField(
              controller: _cmdCtrl,
              decoration: const InputDecoration(
                labelText: 'PowerShell command',
              ),
            ),
            const SizedBox(height: 16),
            FilledButton(
              onPressed: _running ? null : _run,
              child: Text(_running ? 'Running…' : 'Run'),
            ),
            const SizedBox(height: 16),
            const Align(
              alignment: Alignment.centerLeft,
              child: Text('Output:'),
            ),
            const SizedBox(height: 4),
            Expanded(
              child: Container(
                width: double.infinity,
                padding: const EdgeInsets.all(8),
                decoration: BoxDecoration(
                  color: Colors.black,
                  borderRadius: BorderRadius.circular(4),
                ),
                child: SingleChildScrollView(
                  child: SelectableText(
                    _output.isEmpty ? '(no output yet)' : _output,
                    style: const TextStyle(
                      fontFamily: 'monospace',
                      color: Colors.greenAccent,
                      fontSize: 13,
                    ),
                  ),
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }
}
