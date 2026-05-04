# peersh — Android R8/ProGuard keep rules.
#
# Without these, R8's tree shaker strips the gomobile-generated Java
# classes that MainActivity reflects against (Peersh / Session /
# PTYSession / PTYHandler / etc. — see app/android/app/libs/peersh.aar).
# The Go side of the bridge looks up these classes by name through the
# JNI surface gomobile emits, so package-level keep rules are required.

# gomobile runtime support classes (go.Seq.* + helpers).
-keep class go.** { *; }
-keepclassmembers class go.** { *; }
-dontwarn go.**

# Public surface emitted by `gomobile bind github.com/peersh/peersh/mobile-core`.
-keep class peersh.** { *; }
-keepclassmembers class peersh.** { *; }
-dontwarn peersh.**

# Flutter engine + plugin registration. Flutter's own consumer-rules
# already handle most of this, but keep the channel handler classes
# explicit because we wire them by reflection from the platform side.
-keep class io.flutter.embedding.** { *; }
-keep class io.flutter.plugin.** { *; }
-dontwarn io.flutter.**
