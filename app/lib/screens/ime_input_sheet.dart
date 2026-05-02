import 'package:flutter/material.dart';

/// Result returned by the IME bottom sheet.
class ImeInputResult {
  const ImeInputResult({required this.text, required this.appendEnter});
  final String text;
  final bool appendEnter;
}

/// Modal bottom sheet that lets the user compose multi-line input with
/// the IME enabled (PowerShell-friendly), then commit the whole block as
/// one terminal write.
class ImeInputSheet extends StatefulWidget {
  const ImeInputSheet({super.key});

  static Future<ImeInputResult?> show(BuildContext context) {
    return showModalBottomSheet<ImeInputResult>(
      context: context,
      isScrollControlled: true,
      useSafeArea: true,
      builder: (_) => const ImeInputSheet(),
    );
  }

  @override
  State<ImeInputSheet> createState() => _ImeInputSheetState();
}

class _ImeInputSheetState extends State<ImeInputSheet> {
  final _controller = TextEditingController();
  final _focusNode = FocusNode();
  bool _appendEnter = true;

  @override
  void initState() {
    super.initState();
    _controller.addListener(_onChanged);
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (mounted) _focusNode.requestFocus();
    });
  }

  @override
  void dispose() {
    _controller.removeListener(_onChanged);
    _controller.dispose();
    _focusNode.dispose();
    super.dispose();
  }

  void _onChanged() => setState(() {});

  void _send() {
    final text = _controller.text;
    if (text.isEmpty) return;
    Navigator.pop(
      context,
      ImeInputResult(text: text, appendEnter: _appendEnter),
    );
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return AnimatedPadding(
      duration: const Duration(milliseconds: 160),
      curve: Curves.easeOut,
      padding: EdgeInsets.only(bottom: MediaQuery.viewInsetsOf(context).bottom),
      child: Material(
        color: theme.colorScheme.surface,
        child: SafeArea(
          top: false,
          child: Padding(
            padding: const EdgeInsets.fromLTRB(16, 12, 16, 16),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                Row(
                  children: [
                    Expanded(
                      child:
                          Text('IME input', style: theme.textTheme.titleMedium),
                    ),
                    IconButton(
                      icon: const Icon(Icons.close),
                      tooltip: 'Cancel',
                      onPressed: () => Navigator.pop(context),
                    ),
                  ],
                ),
                const SizedBox(height: 8),
                ConstrainedBox(
                  constraints: const BoxConstraints(maxHeight: 220),
                  child: TextField(
                    controller: _controller,
                    focusNode: _focusNode,
                    minLines: 4,
                    maxLines: null,
                    keyboardType: TextInputType.multiline,
                    textInputAction: TextInputAction.newline,
                    decoration: const InputDecoration(
                      border: OutlineInputBorder(),
                      hintText: 'Compose multiline input here',
                    ),
                  ),
                ),
                const SizedBox(height: 8),
                SwitchListTile(
                  contentPadding: EdgeInsets.zero,
                  title: const Text('Append Enter on send'),
                  value: _appendEnter,
                  onChanged: (v) => setState(() => _appendEnter = v),
                ),
                const SizedBox(height: 8),
                FilledButton.icon(
                  icon: const Icon(Icons.send),
                  label: const Text('Send'),
                  onPressed: _controller.text.isEmpty ? null : _send,
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

/// Normalize line endings for the terminal: \r\n / \n → \r, append \r if
/// requested and missing.
String terminalInputFromEditorText(String text, {required bool appendEnter}) {
  var normalized = text.replaceAll('\r\n', '\n').replaceAll('\r', '\n');
  if (appendEnter && !normalized.endsWith('\n')) {
    normalized = '$normalized\n';
  }
  return normalized.replaceAll('\n', '\r');
}
