// iOS entry point. The MethodChannel/EventChannel bridge to mobile-core lives
// in the shared app/shared/apple/PeershBridge.swift (compiled into both the
// iOS and macOS Runner targets). This file just wires it onto the root
// FlutterViewController's messenger.
//
// Requires the gomobile-bound framework: run
//   scripts/build-mobile-core.sh apple
// which produces app/shared/apple/Frameworks/peersh.xcframework, then embed it
// in the Runner target ("Embed & Sign"). Until then `import Peersh` (in
// PeershBridge.swift) will not resolve.

import Flutter
import UIKit

@main
@objc class AppDelegate: FlutterAppDelegate {
  // Strong reference: the bridge owns the channel handlers for the app's life.
  private let bridge = PeershBridge()

  override func application(
    _ application: UIApplication,
    didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]?
  ) -> Bool {
    let controller = window?.rootViewController as! FlutterViewController
    bridge.register(with: controller.binaryMessenger)

    GeneratedPluginRegistrant.register(with: self)
    return super.application(application, didFinishLaunchingWithOptions: launchOptions)
  }

  override func applicationWillTerminate(_ application: UIApplication) {
    bridge.shutdown()
    super.applicationWillTerminate(application)
  }
}
