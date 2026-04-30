// Phase 4a — iOS Method-channel bridge to mobile-core.
//
// Build status: this file is code-complete but the .xcframework needed to
// resolve the `Peersh` import requires running `gomobile bind -target=ios`
// on macOS. On a Windows dev box the iOS build is skipped; see
// scripts/build-mobile-core.sh and docs/architecture.md.

import Flutter
import UIKit
// import Peersh   // Uncomment after gomobile bind -target=ios produces
                   // app/ios/Frameworks/peersh.xcframework and the
                   // framework is added to Runner's "Frameworks, Libraries,
                   // and Embedded Content" in Xcode.

@main
@objc class AppDelegate: FlutterAppDelegate {
  override func application(
    _ application: UIApplication,
    didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]?
  ) -> Bool {
    let controller: FlutterViewController = window?.rootViewController as! FlutterViewController
    let channel = FlutterMethodChannel(
      name: "dev.peersh/bridge",
      binaryMessenger: controller.binaryMessenger
    )
    channel.setMethodCallHandler { (call: FlutterMethodCall, result: @escaping FlutterResult) in
      // The implementations below are the iOS analogues of the Kotlin
      // handler in app/android/app/src/main/kotlin/.../MainActivity.kt.
      // They are commented out until the Peersh framework is available.
      switch call.method {
      case "version":
        // result(PeershPeershVersion())
        result(FlutterError(code: "IOS_BIND_PENDING",
                            message: "Run gomobile bind -target=ios on macOS",
                            details: nil))
      case "echo":
        // let args = call.arguments as? [String: Any] ?? [:]
        // let addr = args["addr"] as? String ?? ""
        // let cmd  = args["command"] as? String ?? ""
        // DispatchQueue.global(qos: .userInitiated).async {
        //   let out = PeershPeershEcho(addr, cmd)
        //   DispatchQueue.main.async { result(out) }
        // }
        result(FlutterError(code: "IOS_BIND_PENDING",
                            message: "Run gomobile bind -target=ios on macOS",
                            details: nil))
      default:
        result(FlutterMethodNotImplemented)
      }
    }

    GeneratedPluginRegistrant.register(with: self)
    return super.application(application, didFinishLaunchingWithOptions: launchOptions)
  }
}
