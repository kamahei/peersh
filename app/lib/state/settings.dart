import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'servers.dart';

/// User-controlled defaults. Persisted via [SecureStore.writeSettings].
class AppSettings {
  const AppSettings({
    this.lineWrap = true,
    this.fontSize = 13.0,
    this.defaultNotifyThresholdSec = 10,
    this.defaultNotifyIdleSec = 0,
  });

  final bool lineWrap;
  final double fontSize;

  /// Default minimum command duration before a prompt-ready notification
  /// fires. Applied to fresh tabs that have no persisted override.
  final int defaultNotifyThresholdSec;

  /// Default output-silence window for the idle heuristic. 0 disables.
  final int defaultNotifyIdleSec;

  AppSettings copyWith({
    bool? lineWrap,
    double? fontSize,
    int? defaultNotifyThresholdSec,
    int? defaultNotifyIdleSec,
  }) =>
      AppSettings(
        lineWrap: lineWrap ?? this.lineWrap,
        fontSize: fontSize ?? this.fontSize,
        defaultNotifyThresholdSec:
            defaultNotifyThresholdSec ?? this.defaultNotifyThresholdSec,
        defaultNotifyIdleSec:
            defaultNotifyIdleSec ?? this.defaultNotifyIdleSec,
      );

  Map<String, dynamic> toJson() => {
        'lineWrap': lineWrap,
        'fontSize': fontSize,
        'defaultNotifyThresholdSec': defaultNotifyThresholdSec,
        'defaultNotifyIdleSec': defaultNotifyIdleSec,
      };

  factory AppSettings.fromJson(Map<String, dynamic> j) => AppSettings(
        lineWrap: j['lineWrap'] as bool? ?? true,
        fontSize: (j['fontSize'] as num?)?.toDouble() ?? 13.0,
        defaultNotifyThresholdSec:
            (j['defaultNotifyThresholdSec'] as num?)?.toInt() ?? 10,
        defaultNotifyIdleSec:
            (j['defaultNotifyIdleSec'] as num?)?.toInt() ?? 0,
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

  Future<void> setDefaultNotifyThresholdSec(int v) async {
    final next = (state.valueOrNull ?? const AppSettings())
        .copyWith(defaultNotifyThresholdSec: v.clamp(1, 600));
    await _store.writeSettings(next.toJson());
    state = AsyncData(next);
  }

  Future<void> setDefaultNotifyIdleSec(int v) async {
    final next = (state.valueOrNull ?? const AppSettings())
        .copyWith(defaultNotifyIdleSec: v.clamp(0, 300));
    await _store.writeSettings(next.toJson());
    state = AsyncData(next);
  }
}

final settingsProvider = AsyncNotifierProvider<AppSettingsNotifier, AppSettings>(
    AppSettingsNotifier.new);
