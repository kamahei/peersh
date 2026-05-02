import 'package:flutter/material.dart';

/// Wrap-vs-scroll-aware monospace output panel.
///
/// In wrap mode the text uses `softWrap: true` and the parent
/// `SingleChildScrollView` only scrolls vertically.
///
/// In scroll mode the text disables wrapping and uses nested scroll
/// views so long lines stay horizontally scrollable.
class LogView extends StatelessWidget {
  const LogView({
    super.key,
    required this.lines,
    required this.lineWrap,
    this.fontSize = 13.0,
    this.partial = '',
    this.errorMessage,
  });

  final List<String> lines;

  /// Trailing partial chunk that has not seen a newline yet.
  final String partial;

  final bool lineWrap;
  final double fontSize;

  /// Non-null shows a red error footer.
  final String? errorMessage;

  @override
  Widget build(BuildContext context) {
    final visible = [
      ...lines,
      if (partial.isNotEmpty) partial,
      if (errorMessage != null) '\nERROR: $errorMessage',
    ];
    final body = SelectableText(
      visible.isEmpty ? '(no output yet)' : visible.join('\n'),
      style: TextStyle(
        fontFamily: 'monospace',
        color: Colors.greenAccent,
        fontSize: fontSize,
      ),
    );

    final container = Container(
      width: double.infinity,
      padding: const EdgeInsets.all(8),
      color: Colors.black,
      child: lineWrap
          ? SingleChildScrollView(child: body)
          : SingleChildScrollView(
              scrollDirection: Axis.horizontal,
              child: SingleChildScrollView(
                child: SizedBox(
                  // Generous width for horizontal scrolling. The terminal
                  // never truly wraps in this mode; the user pans
                  // horizontally to see long lines.
                  width: 1200,
                  child: Wrap(
                    children: [body],
                  ),
                ),
              ),
            ),
    );

    return container;
  }
}
