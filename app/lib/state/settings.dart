import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'servers.dart';

/// User-controlled defaults. Persisted via [SecureStore.writeSettings].
class AppSettings {
  const AppSettings({
    this.lineWrap = true,
    this.fontSize = 13.0,
  });

  final bool lineWrap;
  final double fontSize;

  AppSettings copyWith({bool? lineWrap, double? fontSize}) => AppSettings(
        lineWrap: lineWrap ?? this.lineWrap,
        fontSize: fontSize ?? this.fontSize,
      );

  Map<String, dynamic> toJson() => {
        'lineWrap': lineWrap,
        'fontSize': fontSize,
      };

  factory AppSettings.fromJson(Map<String, dynamic> j) => AppSettings(
        lineWrap: j['lineWrap'] as bool? ?? true,
        fontSize: (j['fontSize'] as num?)?.toDouble() ?? 13.0,
      );
}

class AppSettingsNotifier extends AsyncNotifier<AppSettings> {
  late final _store = ref.read(secureStoreProvider);

  @override
  Future<AppSettings> build() async {
    final raw = await _store.readSettings();
    return raw.isEmpty ? const AppSettings() : AppSettings.fromJson(raw);
  }

  Future<void> setLineWrap(bool v) async {
    final next = (state.valueOrNull ?? const AppSettings()).copyWith(lineWrap: v);
    await _store.writeSettings(next.toJson());
    state = AsyncData(next);
  }

  Future<void> setFontSize(double v) async {
    final next = (state.valueOrNull ?? const AppSettings()).copyWith(fontSize: v);
    await _store.writeSettings(next.toJson());
    state = AsyncData(next);
  }
}

final settingsProvider = AsyncNotifierProvider<AppSettingsNotifier, AppSettings>(
    AppSettingsNotifier.new);
