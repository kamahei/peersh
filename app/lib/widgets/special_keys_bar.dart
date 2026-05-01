import 'package:flutter/material.dart';

/// A horizontal bar of one-tap keys for the terminal screen.
///
/// xterm.dart maps physical-keyboard keystrokes natively, but Android soft
/// keyboards lack Esc / Ctrl / Tab / arrow keys, so a quick-access bar at
/// the bottom of the terminal solves that gap. The leftmost button is the
/// IME input launcher, matching peersh's terminal_workspace layout.
///
/// Byte encodings:
///   Esc        -> 0x1b
///   Tab        -> '\t'
///   ↑ ↓ → ←    -> CSI sequences (xterm-style)
///   ^C ^D ^L ^Z -> control bytes
///   PgUp/PgDn  -> CSI 5~ / CSI 6~
///   Home/End   -> CSI H / CSI F
class SpecialKeysBar extends StatelessWidget {
  const SpecialKeysBar({
    super.key,
    required this.onSendBytes,
    required this.onImeInput,
  });

  final Future<void> Function(List<int> data) onSendBytes;
  final VoidCallback onImeInput;

  @override
  Widget build(BuildContext context) {
    return Material(
      color: Theme.of(context).colorScheme.surfaceContainer,
      child: SafeArea(
        top: false,
        child: SizedBox(
          height: 44,
          child: ListView(
            scrollDirection: Axis.horizontal,
            padding: const EdgeInsets.symmetric(horizontal: 8),
            children: [
              _iconKey(icon: Icons.keyboard_alt_outlined, tooltip: 'IME input', onTap: onImeInput),
              _key(label: 'Tab', onTap: () => onSendBytes('\t'.codeUnits)),
              _key(label: 'Esc', onTap: () => onSendBytes(const [0x1b])),
              _key(label: '↑', onTap: () => onSendBytes(const [0x1b, 0x5b, 0x41])),
              _key(label: '↓', onTap: () => onSendBytes(const [0x1b, 0x5b, 0x42])),
              _key(label: '←', onTap: () => onSendBytes(const [0x1b, 0x5b, 0x44])),
              _key(label: '→', onTap: () => onSendBytes(const [0x1b, 0x5b, 0x43])),
              _key(label: '^C', onTap: () => onSendBytes(const [0x03])),
              _key(label: '^D', onTap: () => onSendBytes(const [0x04])),
              _key(label: '^L', onTap: () => onSendBytes(const [0x0c])),
              _key(label: '^Z', onTap: () => onSendBytes(const [0x1a])),
              _key(label: 'PgUp', onTap: () => onSendBytes(const [0x1b, 0x5b, 0x35, 0x7e])),
              _key(label: 'PgDn', onTap: () => onSendBytes(const [0x1b, 0x5b, 0x36, 0x7e])),
              _key(label: 'Home', onTap: () => onSendBytes(const [0x1b, 0x5b, 0x48])),
              _key(label: 'End', onTap: () => onSendBytes(const [0x1b, 0x5b, 0x46])),
            ],
          ),
        ),
      ),
    );
  }

  Widget _key({required String label, required VoidCallback onTap}) {
    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: 4, vertical: 6),
      child: ElevatedButton(
        style: ElevatedButton.styleFrom(
          minimumSize: const Size(40, 32),
          padding: const EdgeInsets.symmetric(horizontal: 10),
          textStyle: const TextStyle(fontFamily: 'monospace', fontSize: 13),
        ),
        onPressed: onTap,
        child: Text(label),
      ),
    );
  }

  Widget _iconKey({required IconData icon, required String tooltip, required VoidCallback onTap}) {
    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: 4, vertical: 6),
      child: IconButton.filledTonal(
        icon: Icon(icon, size: 20),
        tooltip: tooltip,
        onPressed: onTap,
        constraints: const BoxConstraints(minWidth: 40, minHeight: 32),
        padding: EdgeInsets.zero,
        visualDensity: VisualDensity.compact,
      ),
    );
  }
}
