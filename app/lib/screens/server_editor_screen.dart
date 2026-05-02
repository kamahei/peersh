import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../models/server_entry.dart';
import '../services/discovery.dart';
import '../state/servers.dart';

/// Add or edit a [ServerEntry]. If [existing] is null we are adding;
/// otherwise editing. Discovery prefill only runs when adding.
class ServerEditorScreen extends ConsumerStatefulWidget {
  const ServerEditorScreen({super.key, this.existing});

  final ServerEntry? existing;

  @override
  ConsumerState<ServerEditorScreen> createState() => _ServerEditorScreenState();
}

class _ServerEditorScreenState extends ConsumerState<ServerEditorScreen> {
  final _name = TextEditingController();
  final _host = TextEditingController();
  final _wsUrl = TextEditingController();
  final _userId = TextEditingController();
  final _psk = TextEditingController();
  final _target = TextEditingController();
  final _stun = TextEditingController(text: 'stun.l.google.com:19302');
  bool _busy = false;
  String? _discoveryStatus;
  ServerAuthMode _authMode = ServerAuthMode.psk;

  @override
  void initState() {
    super.initState();
    final e = widget.existing;
    if (e != null) {
      _name.text = e.name;
      _wsUrl.text = e.wsUrl;
      _userId.text = e.userId;
      _psk.text = e.pskHex;
      _target.text = e.targetDeviceId;
      _stun.text = e.stunServer;
      _authMode = e.authMode;
    }
  }

  @override
  void dispose() {
    _name.dispose();
    _host.dispose();
    _wsUrl.dispose();
    _userId.dispose();
    _psk.dispose();
    _target.dispose();
    _stun.dispose();
    super.dispose();
  }

  Future<void> _runDiscovery() async {
    final input = _host.text.trim();
    if (input.isEmpty) return;
    setState(() {
      _busy = true;
      _discoveryStatus = 'Looking up /.well-known/peersh.json…';
    });
    final result = await fetchDiscovery(input);
    if (!mounted) return;
    setState(() {
      _busy = false;
      final doc = result.doc;
      if (doc == null) {
        _discoveryStatus = result.error ?? 'No discovery doc returned. Fill ws_url manually.';
        return;
      }
      if (doc.wsUrl.isNotEmpty) _wsUrl.text = doc.wsUrl;
      if (doc.stunServers.isNotEmpty) _stun.text = doc.stunServers.first;
      // Auto-pick auth mode from advertised providers.
      if (doc.authProviders.contains('firebase')) {
        _authMode = ServerAuthMode.firebase;
      } else if (doc.authProviders.contains('psk')) {
        _authMode = ServerAuthMode.psk;
      }
      _discoveryStatus = 'Loaded discovery v${doc.version}.';
    });
  }

  Future<void> _save() async {
    final entry = ServerEntry(
      id: widget.existing?.id ?? DateTime.now().microsecondsSinceEpoch.toString(),
      name: _name.text.trim().isEmpty ? _wsUrl.text.trim() : _name.text.trim(),
      wsUrl: _wsUrl.text.trim(),
      userId: _userId.text.trim(),
      pskHex: _psk.text.trim(),
      targetDeviceId: _target.text.trim(),
      stunServer: _stun.text.trim(),
      authMode: _authMode,
    );
    final notifier = ref.read(serversProvider.notifier);
    if (widget.existing == null) {
      await notifier.add(entry);
    } else {
      await notifier.replace(entry);
    }
    if (!mounted) return;
    Navigator.pop(context);
  }

  bool get _canSave {
    if (_wsUrl.text.trim().isEmpty) return false;
    if (_authMode == ServerAuthMode.psk) {
      // PSK mode: target device id is required (no Firestore-backed
      // device picker fallback).
      return _target.text.trim().isNotEmpty &&
          _userId.text.trim().isNotEmpty &&
          _psk.text.trim().isNotEmpty;
    }
    // Firebase mode: target device id is optional — left empty, the
    // connect flow surfaces a picker that reads users/{uid}/devices.
    return true;
  }

  @override
  Widget build(BuildContext context) {
    final isAdd = widget.existing == null;
    return Scaffold(
      appBar: AppBar(
        title: Text(isAdd ? 'Add server' : 'Edit server'),
        actions: [
          TextButton(
            onPressed: !_canSave || _busy ? null : _save,
            child: const Text('Save'),
          ),
        ],
      ),
      body: ListView(
        padding: const EdgeInsets.all(16),
        children: [
          TextField(
            controller: _name,
            decoration: const InputDecoration(
              labelText: 'Display name (optional)',
            ),
          ),
          if (isAdd) ...[
            const SizedBox(height: 16),
            const Text('Discover from hostname'),
            const SizedBox(height: 4),
            Row(
              children: [
                Expanded(
                  child: TextField(
                    controller: _host,
                    decoration: const InputDecoration(
                      labelText: 'host or URL',
                      hintText: 'signaling.example.com',
                    ),
                    onChanged: (_) => setState(() {}),
                  ),
                ),
                const SizedBox(width: 8),
                FilledButton.tonal(
                  onPressed: _busy || _host.text.trim().isEmpty
                      ? null
                      : _runDiscovery,
                  child: const Text('Lookup'),
                ),
              ],
            ),
            if (_discoveryStatus != null) ...[
              const SizedBox(height: 4),
              Text(
                _discoveryStatus!,
                style: Theme.of(context).textTheme.bodySmall,
              ),
            ],
          ],
          const SizedBox(height: 16),
          TextField(
            controller: _wsUrl,
            decoration: const InputDecoration(
              labelText: 'ws / wss URL',
              hintText: 'wss://signaling.example.com/ws',
            ),
            onChanged: (_) => setState(() {}),
          ),
          const SizedBox(height: 16),
          DropdownButtonFormField<ServerAuthMode>(
            value: _authMode,
            decoration: const InputDecoration(labelText: 'Auth provider'),
            items: const [
              DropdownMenuItem(
                value: ServerAuthMode.psk,
                child: Text('PSK (self-host)'),
              ),
              DropdownMenuItem(
                value: ServerAuthMode.firebase,
                child: Text('Firebase (Google sign-in)'),
              ),
            ],
            onChanged: (v) => setState(() => _authMode = v ?? ServerAuthMode.psk),
          ),
          if (_authMode == ServerAuthMode.psk) ...[
            const SizedBox(height: 16),
            TextField(
              controller: _userId,
              decoration: const InputDecoration(labelText: 'User ID'),
              onChanged: (_) => setState(() {}),
            ),
            const SizedBox(height: 16),
            TextField(
              controller: _psk,
              decoration: const InputDecoration(
                labelText: 'PSK (hex)',
                hintText: '64 hex chars',
              ),
              obscureText: true,
              onChanged: (_) => setState(() {}),
            ),
          ],
          const SizedBox(height: 16),
          TextField(
            controller: _target,
            decoration: InputDecoration(
              labelText: _authMode == ServerAuthMode.firebase
                  ? 'Target device_id (optional — picker on connect)'
                  : 'Target device_id',
              hintText: _authMode == ServerAuthMode.firebase
                  ? 'Leave empty to choose at connect time'
                  : '16 base32 chars from peershd startup log',
            ),
            onChanged: (_) => setState(() {}),
          ),
          const SizedBox(height: 16),
          TextField(
            controller: _stun,
            decoration: const InputDecoration(
              labelText: 'STUN server (empty disables)',
            ),
          ),
        ],
      ),
    );
  }
}
