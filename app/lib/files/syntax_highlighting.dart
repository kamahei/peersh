// Language mapping for files commonly inspected from a Windows shell:
// PowerShell scripts, JSON / YAML config, and source files in common
// project languages.

import 'package:flutter/painting.dart';
import 'package:highlighting/highlighting.dart';
import 'package:highlighting/languages/all.dart';

/// Returns the highlighting language id (e.g. "powershell", "json") for
/// the given file path, or null when the extension is unrecognised or
/// the package does not ship a definition for it.
String? highlightLanguageForPath(String path) {
  final name = path.split(RegExp(r'[\\/]')).last.toLowerCase();
  final dot = name.lastIndexOf('.');
  final ext = dot >= 0 ? name.substring(dot + 1) : '';

  switch (name) {
    case 'dockerfile':
      return 'dockerfile';
    case 'makefile':
    case 'gnumakefile':
      return 'makefile';
    case 'cmakelists.txt':
      return 'cmake';
  }

  final language = switch (ext) {
    'ps1' || 'psm1' || 'psd1' => 'powershell',
    'sh' || 'bash' || 'zsh' => 'shell',
    'bat' || 'cmd' => 'dos',
    'json' || 'jsonc' => 'json',
    'yaml' || 'yml' => 'yaml',
    'xml' || 'xaml' || 'csproj' || 'props' || 'targets' => 'xml',
    'html' || 'htm' => 'xml',
    'css' => 'css',
    'scss' => 'scss',
    'js' || 'mjs' || 'cjs' || 'jsx' => 'javascript',
    'ts' || 'tsx' => 'typescript',
    'md' || 'markdown' => 'markdown',
    'dart' => 'dart',
    'go' => 'go',
    'py' || 'pyw' => 'python',
    'rs' => 'rust',
    'java' => 'java',
    'kt' || 'kts' => 'kotlin',
    'swift' => 'swift',
    'c' || 'h' || 'cc' || 'cpp' || 'cxx' || 'hpp' || 'hxx' => 'cpp',
    'cs' => 'cs',
    'sql' => 'sql',
    'diff' || 'patch' => 'diff',
    'ini' || 'toml' => 'ini',
    'properties' => 'properties',
    'mk' => 'makefile',
    'dockerfile' => 'dockerfile',
    _ => null,
  };

  if (language == null || !allLanguages.containsKey(language)) return null;
  return language;
}

/// Builds the TextSpan list rendering [content] with the given
/// highlighting language and theme. The theme map comes from the
/// flutter_highlighting package (e.g. `themes/github-dark.dart` exports
/// a `Map<String, TextStyle>`).
List<TextSpan> syntaxHighlightSpans({
  required String content,
  required String language,
  required Map<String, TextStyle> theme,
  required TextStyle baseStyle,
}) {
  final languageDefinition = allLanguages[language];
  if (languageDefinition == null) return [TextSpan(text: content)];
  highlight.registerLanguage(languageDefinition, id: language);
  final nodes =
      highlight.parse(content, languageId: language).nodes ?? const [];
  final spans = <TextSpan>[];
  for (final node in nodes) {
    _appendNodeSpan(spans, node, theme, baseStyle);
  }
  return spans;
}

void _appendNodeSpan(
  List<TextSpan> spans,
  Node node,
  Map<String, TextStyle> theme,
  TextStyle baseStyle,
) {
  final nodeStyle = _styleForNode(node, theme, baseStyle);
  final value = node.value;
  if (value != null) {
    spans.add(TextSpan(text: value, style: nodeStyle));
    return;
  }
  final children = <TextSpan>[];
  for (final child in node.children) {
    _appendNodeSpan(children, child, theme, baseStyle);
  }
  spans.add(TextSpan(children: children, style: nodeStyle));
}

TextStyle? _styleForNode(
  Node node,
  Map<String, TextStyle> theme,
  TextStyle baseStyle,
) {
  final className = node.className;
  if (className == null) return null;
  final style = theme[className];
  if (style == null) return null;
  return baseStyle.merge(style);
}
