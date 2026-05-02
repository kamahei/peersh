// Phase 5b — Pair PC screen.
//
// Surfaced from Settings. Calls the mintPairingCode Cloud Function with
// a fresh Firebase ID token and shows the resulting 6-digit code with a
// countdown to expiry. The PC operator types this into peershd:
//
//   peershd.exe -pair-code 123456 ...
//
// On expiry the user can tap "Generate new code" to mint another.

import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../services/flavor.dart' as flavor;
import '../services/pairing_service.dart';
import 'firebase_signin.dart';

final pairingServiceProvider = Provider<PairingService>(
  (ref) => PairingService(),
);

class PairPcScreen extends ConsumerStatefulWidget {
  const PairPcScreen({super.key});

  @override
  ConsumerState<PairPcScreen> createState() => _PairPcScreenState();
}

class _PairPcScreenState extends ConsumerState<PairPcScreen> {
  PairingCode? _code;
  String? _error;
  bool _busy = false;
  Timer? _ticker;
  Duration _remaining = Duration.zero;

  @override
  void dispose() {
    _ticker?.cancel();
    super.dispose();
  }

  Future<void> _generate() async {
    if (!flavor.kFirebaseInitialized) {
      setState(() => _error =
          'Firebase is not configured in this build. Run flutterfire configure.');
      return;
    }
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final idToken = await ensureSignedInAndGetIdToken(context, ref);
      if (idToken == null) {
        setState(() => _busy = false);
        return;
      }
      final code = await ref
          .read(pairingServiceProvider)
          .mintCode(firebaseIdToken: idToken);
      _ticker?.cancel();
      setState(() {
        _code = code;
        _remaining = code.expiresAt.difference(DateTime.now());
      });
      _ticker = Timer.periodic(const Duration(seconds: 1), (t) {
        final left = code.expiresAt.difference(DateTime.now());
        if (left.isNegative) {
          t.cancel();
          if (mounted) setState(() => _remaining = Duration.zero);
        } else {
          if (mounted) setState(() => _remaining = left);
        }
      });
    } catch (e) {
      if (mounted) setState(() => _error = '$e');
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  String _format(Duration d) {
    final m = d.inMinutes.toString().padLeft(2, '0');
    final s = (d.inSeconds % 60).toString().padLeft(2, '0');
    return '$m:$s';
  }

  @override
  Widget build(BuildContext context) {
    final code = _code;
    final expired = code != null && _remaining == Duration.zero;
    return Scaffold(
      appBar: AppBar(title: const Text('Pair PC')),
      body: Padding(
        padding: const EdgeInsets.all(24),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            const Text(
              'Generate a one-time code, then run on your PC:',
              style: TextStyle(fontWeight: FontWeight.w600),
            ),
            const SizedBox(height: 8),
            const SelectableText(
              '  peershd.exe -pair-code <code> '
              '-firebase-project-id <project-id> '
              '-firebase-api-key <api-key> '
              '-signaling <wss-url>',
              style: TextStyle(fontFamily: 'monospace', fontSize: 12),
            ),
            const SizedBox(height: 24),
            if (code == null)
              FilledButton.icon(
                onPressed: _busy ? null : _generate,
                icon: const Icon(Icons.qr_code_2),
                label: Text(_busy ? 'Generating…' : 'Generate code'),
              )
            else ...[
              Center(
                child: SelectableText(
                  code.code,
                  style: const TextStyle(
                    fontFamily: 'monospace',
                    fontSize: 56,
                    fontWeight: FontWeight.bold,
                    letterSpacing: 6,
                  ),
                ),
              ),
              const SizedBox(height: 8),
              Center(
                child: Text(
                  expired
                      ? 'Code expired'
                      : 'Expires in ${_format(_remaining)}',
                  style: TextStyle(
                    color: expired
                        ? Theme.of(context).colorScheme.error
                        : Theme.of(context).colorScheme.onSurfaceVariant,
                  ),
                ),
              ),
              const SizedBox(height: 16),
              Wrap(
                alignment: WrapAlignment.center,
                spacing: 12,
                children: [
                  OutlinedButton.icon(
                    onPressed: () async {
                      await Clipboard.setData(ClipboardData(text: code.code));
                      if (!context.mounted) return;
                      ScaffoldMessenger.of(context).showSnackBar(
                        const SnackBar(content: Text('Copied')),
                      );
                    },
                    icon: const Icon(Icons.copy),
                    label: const Text('Copy'),
                  ),
                  FilledButton.tonalIcon(
                    onPressed: _busy ? null : _generate,
                    icon: const Icon(Icons.refresh),
                    label: const Text('New code'),
                  ),
                ],
              ),
            ],
            if (_error != null) ...[
              const SizedBox(height: 16),
              SelectableText(
                _error!,
                style: TextStyle(
                  color: Theme.of(context).colorScheme.error,
                  fontFamily: 'monospace',
                ),
              ),
            ],
            const SizedBox(height: 24),
            const Divider(),
            const SizedBox(height: 8),
            const Text(
              'How this works',
              style: TextStyle(fontWeight: FontWeight.w600),
            ),
            const SizedBox(height: 8),
            const Text(
              'The code lets your PC mint Firebase tokens for your account '
              'without distributing a service-account JSON. The PC keeps '
              'only a refresh token scoped to your account.',
            ),
          ],
        ),
      ),
    );
  }
}
