// Phase 4b — iOS Method-channel + EventChannel bridge to mobile-core.
//
// Build status: this file is code-complete but the .xcframework needed to
// resolve the `Peersh*` symbols requires running `gomobile bind -target=ios`
// on macOS. On a Windows dev box the iOS build is skipped; see
// scripts/build-mobile-core.sh and docs/design/architecture.md.
//
// All session-lifecycle handlers below currently return
// FlutterError("IOS_BIND_PENDING") so a misconfigured iOS run gets a clear
// failure instead of a crash. After running gomobile bind on macOS,
// uncomment the `// import Peersh` line and the bodies in each method.

import Flutter
import UIKit
// import Peersh

@main
@objc class AppDelegate: FlutterAppDelegate {
  private let controlChannelName = "dev.peersh/bridge"
  private let eventChannelName = "dev.peersh/session/events"

  private var sink: FlutterEventSink?
  // private var sessions: [Int: PeershSession] = [:]   // <- after gomobile bind
  private var nextSessionId: Int = 1
  private let workerQueue = DispatchQueue(label: "dev.peersh.bridge.worker", qos: .userInitiated)

  override func application(
    _ application: UIApplication,
    didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]?
  ) -> Bool {
    let controller: FlutterViewController = window?.rootViewController as! FlutterViewController
    let messenger = controller.binaryMessenger

    let eventChannel = FlutterEventChannel(name: eventChannelName, binaryMessenger: messenger)
    eventChannel.setStreamHandler(SinkAdapter(setSink: { [weak self] in self?.sink = $0 }))

    let methodChannel = FlutterMethodChannel(name: controlChannelName, binaryMessenger: messenger)
    methodChannel.setMethodCallHandler { [weak self] (call: FlutterMethodCall, result: @escaping FlutterResult) in
      guard let self = self else { result(FlutterError(code: "NO_SELF", message: "AppDelegate gone", details: nil)); return }

      switch call.method {
      case "version":
        // result(PeershPeershVersion())
        result(FlutterError(code: "IOS_BIND_PENDING",
                            message: "Run gomobile bind -target=ios on macOS",
                            details: nil))

      case "echo", "openDirectSession", "openSignalingSession", "exec", "readFile", "closeSession",
           "openPTY", "ptyInput", "ptyResize", "closePTY",
           "getCwd", "listSessionFiles", "readSessionFile",
           "listPTYs", "killPTY":
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

private class SinkAdapter: NSObject, FlutterStreamHandler {
  let setSink: (FlutterEventSink?) -> Void
  init(setSink: @escaping (FlutterEventSink?) -> Void) { self.setSink = setSink }

  func onListen(withArguments arguments: Any?, eventSink events: @escaping FlutterEventSink) -> FlutterError? {
    setSink(events)
    return nil
  }
  func onCancel(withArguments arguments: Any?) -> FlutterError? {
    setSink(nil)
    return nil
  }
}
