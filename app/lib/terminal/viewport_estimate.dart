import 'package:flutter/material.dart';

/// Roughly estimate how many character cells fit in the area available to
/// a [TerminalView] given the current screen and the configured terminal
/// font size.
///
/// The estimate is intentionally rough — we do not have access to the
/// exact monospace cell width xterm.dart will compute from the rendered
/// font. We use this ONLY to pre-size the host's ConPTY when the PTY
/// session is created, before xterm has had a chance to measure itself
/// and emit an authoritative resize.
///
/// Why pre-size at all? PowerShell's PSReadLine module caches
/// `[Console]::WindowWidth` it observes at startup and may not always
/// pick up subsequent ConPTY resizes promptly. Starting the ConPTY close
/// to the real viewport avoids the visible "PowerShell formatted for the
/// wrong width" symptom — text wrapping at column 80 even though the
/// phone screen is far narrower.
({int cols, int rows}) estimateViewportCells(
  BuildContext context, {
  required double fontSize,
}) {
  final mq = MediaQuery.of(context);
  // Empirical cell metrics for the Flutter monospace stack at the
  // configured fontSize. xterm.dart will measure exactly later; this
  // estimate just needs to be in the right ballpark.
  final cellW = fontSize * 0.6;
  final cellH = fontSize * 1.2;

  // Subtract chrome that is likely to sit between the screen edge and
  // the terminal: top status bar, AppBar (~56), soft-key bar (~44),
  // bottom safe area, IME if up.
  final chromeH =
      mq.padding.top + 56 + 44 + mq.padding.bottom + mq.viewInsets.bottom;
  final availW = mq.size.width;
  final availH = (mq.size.height - chromeH).clamp(80.0, double.infinity);

  final cols = (availW / cellW).floor().clamp(20, 500);
  final rows = (availH / cellH).floor().clamp(5, 200);
  return (cols: cols, rows: rows);
}
