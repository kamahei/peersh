// Shared Apple (iOS + macOS) MethodChannel/EventChannel bridge to mobile-core.
//
// This file is compiled into BOTH the iOS Runner and the macOS Runner target
// (added by reference, not copied). The gomobile-bound `Peersh` framework
// exposes the same Objective-C surface on both platforms, so the bridge logic
// is 100% shared; only the Flutter module import differs (Flutter vs
// FlutterMacOS), handled by the #if below.
//
// It is a direct port of the Android reference implementation,
// app/android/app/src/main/kotlin/dev/peersh/app/MainActivity.kt — keep the two
// in sync. Session/PTY lifecycle, the reattach buffering shim, and the event
// shapes on the `dev.peersh/session/events` channel must match exactly, since
// the Dart side (app/lib/bridge.dart) parses both channels identically across
// platforms.
//
// gomobile symbol naming (see mobile-core/doc.go): the Go package `peersh`
// binds to free functions `Peersh<Func>()` (e.g. PeershVersion,
// PeershOpenSignalingSessionV2), classes `Peersh<Type>` (PeershSession,
// PeershPTYSession, PeershFileEntryList, ...), and protocols `Peersh<Iface>`
// (PeershOutput, PeershPTYHandler). Go methods returning `error` import as
// Swift `throws`. Go `[]byte` <-> Swift `Data`; Go `int` <-> `Int`; Go `int64`
// <-> `Int64`; Go `int32` <-> `Int32`.
//
// NOTE: the `import Peersh` line and the exact selector spellings can only be
// verified after `scripts/build-mobile-core.sh apple` produces
// app/shared/apple/Frameworks/peersh.xcframework and it is embedded in the
// Runner target. If the first compile flags a selector mismatch, reconcile
// against the generated Peersh.framework/Headers/Peersh.h — the logic here is
// authoritative, the spellings follow gomobile's documented convention.

#if os(iOS)
import Flutter
#elseif os(macOS)
import FlutterMacOS
#endif
import Foundation
import Peersh

final class PeershBridge: NSObject, FlutterStreamHandler {
    private let controlChannelName = "dev.peersh/bridge"
    private let eventChannelName = "dev.peersh/session/events"

    // Blocking gomobile calls run here; FlutterResult is always delivered back
    // on the main (platform) thread, which Flutter requires.
    private let worker = DispatchQueue(label: "dev.peersh.bridge.worker",
                                       qos: .userInitiated,
                                       attributes: .concurrent)

    // Shared state guarded by `lock` (Kotlin used ConcurrentHashMap +
    // AtomicInteger; a single lock is sufficient here and keeps the port
    // simple). Handlers/forwarders never touch these maps.
    private let lock = NSLock()
    private var sessions: [Int: PeershSession] = [:]
    private var ptys: [Int64: PeershPTYSession] = [:]
    private var sessionForPty: [Int64: Int] = [:]
    private var nextSessionId = 1

    // EventChannel sink. Only ever read/written on the main thread.
    private var sink: FlutterEventSink?

    // Persistent ed25519 key directory. Reusing the same directory across
    // dials keeps the mTLS-derived device_id stable, which is what lets the
    // host reattach a client to its earlier shells after a QUIC reconnect.
    // Placed under Application Support and excluded from iCloud/iTunes backup
    // to mirror Android's noBackupFilesDir semantics (each device must own its
    // identity; syncing it would be both a privacy leak and a correctness bug).
    private lazy var deviceKeyDir: String = {
        let fm = FileManager.default
        let base: URL
        do {
            base = try fm.url(for: .applicationSupportDirectory,
                              in: .userDomainMask,
                              appropriateFor: nil,
                              create: true)
        } catch {
            // Last-resort fallback: temp dir gives an ephemeral identity
            // rather than crashing. Reattach won't persist but sessions work.
            return NSTemporaryDirectory() + "peersh-device-key"
        }
        var dir = base.appendingPathComponent("peersh-device-key", isDirectory: true)
        try? fm.createDirectory(at: dir, withIntermediateDirectories: true)
        var values = URLResourceValues()
        values.isExcludedFromBackup = true
        try? dir.setResourceValues(values)
        return dir.path
    }()

    // MARK: - Registration

    /// Wires the control + event channels onto `messenger`. Called from the
    /// iOS AppDelegate and the macOS MainFlutterWindow. Keep a strong
    /// reference to the returned bridge for the lifetime of the app.
    func register(with messenger: FlutterBinaryMessenger) {
        let events = FlutterEventChannel(name: eventChannelName, binaryMessenger: messenger)
        events.setStreamHandler(self)

        let control = FlutterMethodChannel(name: controlChannelName, binaryMessenger: messenger)
        control.setMethodCallHandler { [weak self] call, result in
            self?.handle(call, result)
        }
    }

    // MARK: - FlutterStreamHandler

    func onListen(withArguments arguments: Any?, eventSink events: @escaping FlutterEventSink) -> FlutterError? {
        sink = events
        return nil
    }

    func onCancel(withArguments arguments: Any?) -> FlutterError? {
        sink = nil
        return nil
    }

    // MARK: - Locking helpers

    private func withLock<T>(_ body: () -> T) -> T {
        lock.lock(); defer { lock.unlock() }
        return body()
    }

    private func allocSessionId() -> Int {
        withLock { let v = nextSessionId; nextSessionId += 1; return v }
    }

    // MARK: - Event emission

    /// Posts an event map to the Dart EventChannel on the main thread.
    fileprivate func emit(_ event: [String: Any]) {
        DispatchQueue.main.async { [weak self] in
            self?.sink?(event)
        }
    }

    // MARK: - Method dispatch

    private func handle(_ call: FlutterMethodCall, _ result: @escaping FlutterResult) {
        let args = call.arguments as? [String: Any] ?? [:]
        func str(_ k: String) -> String { args[k] as? String ?? "" }
        func i32(_ k: String) -> Int32 { (args[k] as? NSNumber)?.int32Value ?? 0 }
        func i64(_ k: String) -> Int64 { (args[k] as? NSNumber)?.int64Value ?? 0 }
        func intOr(_ k: String, _ d: Int) -> Int { (args[k] as? NSNumber)?.intValue ?? d }
        func boolOr(_ k: String, _ d: Bool) -> Bool { (args[k] as? NSNumber)?.boolValue ?? d }
        func bytes(_ k: String) -> Data { (args[k] as? FlutterStandardTypedData)?.data ?? Data() }

        switch call.method {
        case "version":
            result(PeershVersion())

        case "echo":
            let addr = str("addr"), cmd = str("command")
            worker.async {
                let out = PeershEcho(addr, cmd)
                DispatchQueue.main.async { result(out) }
            }

        case "openDirectSession":
            let addr = str("addr")
            worker.async { [weak self] in
                guard let self = self else { return }
                // gomobile's package-level funcs import as non-throwing with a
                // trailing NSError** out-param (unlike its instance methods).
                var err: NSError?
                guard let s = PeershOpenDirectSessionWithKey(addr, self.deviceKeyDir, &err) else {
                    DispatchQueue.main.async {
                        result(FlutterError(code: "OPEN_FAILED", message: err?.localizedDescription ?? "open failed", details: nil))
                    }
                    return
                }
                let id = self.allocSessionId()
                self.withLock { self.sessions[id] = s }
                DispatchQueue.main.async { result(id) }
            }

        case "openSignalingSession":
            let signaling = str("signaling"), user = str("user"), psk = str("psk")
            let target = str("target"), stun = str("stun"), idle = i32("idleTimeoutSec")
            worker.async { [weak self] in
                guard let self = self else { return }
                var err: NSError?
                guard let s = PeershOpenSignalingSessionV2(
                    signaling, user, psk, target, stun, self.deviceKeyDir, idle, &err) else {
                    DispatchQueue.main.async {
                        result(FlutterError(code: "OPEN_FAILED", message: err?.localizedDescription ?? "open failed", details: nil))
                    }
                    return
                }
                let id = self.allocSessionId()
                self.withLock { self.sessions[id] = s }
                DispatchQueue.main.async { result(id) }
            }

        case "openFirebaseSignalingSession":
            let signaling = str("signaling"), idToken = str("idToken")
            let appCheck = str("appCheckToken"), target = str("target")
            let stun = str("stun"), idle = i32("idleTimeoutSec")
            worker.async { [weak self] in
                guard let self = self else { return }
                var err: NSError?
                guard let s = PeershOpenFirebaseSignalingSessionV2(
                    signaling, idToken, appCheck, target, stun, self.deviceKeyDir, idle, &err) else {
                    DispatchQueue.main.async {
                        result(FlutterError(code: "OPEN_FAILED", message: err?.localizedDescription ?? "open failed", details: nil))
                    }
                    return
                }
                let id = self.allocSessionId()
                self.withLock { self.sessions[id] = s }
                DispatchQueue.main.async { result(id) }
            }

        case "exec":
            let sessionId = intOr("sessionId", 0)
            let command = str("command")
            guard let s = withLock({ sessions[sessionId] }) else {
                result(FlutterError(code: "UNKNOWN_SESSION", message: "no session for id=\(sessionId)", details: nil))
                return
            }
            worker.async { [weak self] in
                guard let self = self else { return }
                let handler = SessionOutputForwarder(sessionId: sessionId, bridge: self)
                do {
                    try s.exec(command, handler: handler)
                } catch {
                    // Defensive: Go returns errors via OnDone, but any
                    // unexpected throw is surfaced as a done event too.
                    handler.forwardDone(error.localizedDescription)
                }
                DispatchQueue.main.async { result(nil) }
            }

        case "readFile":
            let sessionId = intOr("sessionId", 0)
            let path = str("path")
            guard let s = withLock({ sessions[sessionId] }) else {
                result(FlutterError(code: "UNKNOWN_SESSION", message: "no session for id=\(sessionId)", details: nil))
                return
            }
            worker.async {
                let out = s.readFile(path)
                DispatchQueue.main.async { result(out) }
            }

        case "closeSession":
            let sessionId = intOr("sessionId", 0)
            let s = withLock { sessions.removeValue(forKey: sessionId) }
            guard let s = s else { result(nil); return }
            worker.async {
                try? s.close()
                DispatchQueue.main.async { result(nil) }
            }

        // Android-only foreground-service / notification methods. On Apple these
        // are no-ops with sensible defaults (the Dart bridge tolerates either a
        // success or a thrown error, but returning success is cleaner).
        case "fgServiceStart", "fgServiceStop", "requestNotifications", "openNotificationSettings":
            result(nil)

        case "notificationsEnabled":
            result(true)

        case "openPTY":
            let sessionId = intOr("sessionId", 0)
            let command = str("command")
            let cols = intOr("cols", 80), rows = intOr("rows", 24)
            let reattachHandle = str("reattachHandle")
            guard let s = withLock({ sessions[sessionId] }) else {
                result(FlutterError(code: "UNKNOWN_SESSION", message: "no session for id=\(sessionId)", details: nil))
                return
            }
            worker.async { [weak self] in
                guard let self = self else { return }
                do {
                    // Buffering shim: openPTY[Reattach] needs a PTYHandler
                    // before we have the host-assigned ptyId (only known after
                    // the call returns), but on reattach the host starts
                    // streaming the scrollback ring buffer the moment the ack
                    // lands. Without this buffer those replay bytes would arrive
                    // while the real handler is still nil and be silently
                    // dropped — which is why a freshly reconnected client used
                    // to render a blank terminal until new output arrived.
                    let temp = BufferingPTYHandler()
                    let p: PeershPTYSession
                    if !reattachHandle.isEmpty {
                        p = try s.openPTYReattach(reattachHandle, cols: cols, rows: rows, handler: temp)
                    } else {
                        p = try s.openPTY(command, cols: cols, rows: rows, handler: temp)
                    }
                    // gomobile renames Go's PTYSession.ID() to `id_` (ObjC
                    // reserves `id`).
                    let ptyId = p.id_()
                    temp.activate(PTYEventForwarder(ptyId: ptyId, bridge: self))
                    self.withLock {
                        self.ptys[ptyId] = p
                        self.sessionForPty[ptyId] = sessionId
                    }
                    // Best-effort: poll the host-assigned reattach handle for up
                    // to 2 s so the Dart side can persist it. The ack arrives
                    // within a few hundred ms in practice.
                    var handle = ""
                    for _ in 0..<20 {
                        handle = p.handle()
                        if !handle.isEmpty { break }
                        Thread.sleep(forTimeInterval: 0.1)
                    }
                    let out: [String: Any] = ["ptyId": ptyId, "handle": handle]
                    DispatchQueue.main.async { result(out) }
                } catch {
                    DispatchQueue.main.async {
                        result(FlutterError(code: "PTY_OPEN_FAILED", message: error.localizedDescription, details: nil))
                    }
                }
            }

        case "listPTYs":
            let sessionId = intOr("sessionId", 0)
            guard let s = withLock({ sessions[sessionId] }) else {
                result(FlutterError(code: "UNKNOWN_SESSION", message: "no session for id=\(sessionId)", details: nil))
                return
            }
            worker.async {
                do {
                    let list = s.listPTYs()
                    let total = Int(list?.len() ?? 0)
                    var items: [[String: Any]] = []
                    items.reserveCapacity(total)
                    for i in 0..<total {
                        guard let e = list?.get(i) else { continue }
                        items.append([
                            "handle": e.handle,
                            "command": e.command,
                            "attached": e.attached,
                            "attachedCount": e.attachedCount,
                            "cwd": e.cwd,
                            "lastSeenUnixMs": e.lastSeenUnixMs,
                        ])
                    }
                    DispatchQueue.main.async { result(items) }
                }
            }

        case "killPTY":
            let sessionId = intOr("sessionId", 0)
            let handle = str("handle")
            guard let s = withLock({ sessions[sessionId] }) else {
                result(FlutterError(code: "UNKNOWN_SESSION", message: "no session for id=\(sessionId)", details: nil))
                return
            }
            worker.async {
                let err = s.killPTY(handle)
                DispatchQueue.main.async { result(err) }
            }

        case "ptyInput":
            let ptyId = i64("ptyId")
            let data = bytes("data")
            guard let p = withLock({ ptys[ptyId] }) else {
                result(FlutterError(code: "UNKNOWN_PTY", message: "no pty for id=\(ptyId)", details: nil))
                return
            }
            worker.async {
                do {
                    try p.write(data)
                    DispatchQueue.main.async { result(nil) }
                } catch {
                    DispatchQueue.main.async {
                        result(FlutterError(code: "PTY_WRITE_FAILED", message: error.localizedDescription, details: nil))
                    }
                }
            }

        case "ptyResize":
            let ptyId = i64("ptyId")
            let cols = intOr("cols", 80), rows = intOr("rows", 24)
            guard let p = withLock({ ptys[ptyId] }) else {
                result(FlutterError(code: "UNKNOWN_PTY", message: "no pty for id=\(ptyId)", details: nil))
                return
            }
            worker.async {
                do {
                    try p.resize(cols, rows: rows)
                    DispatchQueue.main.async { result(nil) }
                } catch {
                    DispatchQueue.main.async {
                        result(FlutterError(code: "PTY_RESIZE_FAILED", message: error.localizedDescription, details: nil))
                    }
                }
            }

        case "ptyNotificationConfig":
            let ptyId = i64("ptyId")
            let enabled = boolOr("enabled", false)
            let threshold = intOr("thresholdSeconds", 0)
            let idle = intOr("idleSeconds", 0)
            let tabLabel = str("tabLabel")
            let mobileDeviceId = str("mobileDeviceId")
            guard let p = withLock({ ptys[ptyId] }) else {
                result(FlutterError(code: "UNKNOWN_PTY", message: "no pty for id=\(ptyId)", details: nil))
                return
            }
            worker.async {
                do {
                    try p.sendNotificationConfig(enabled,
                                                 thresholdSeconds: threshold,
                                                 idleSeconds: idle,
                                                 tabLabel: tabLabel,
                                                 mobileDeviceID: mobileDeviceId)
                    DispatchQueue.main.async { result(nil) }
                } catch {
                    DispatchQueue.main.async {
                        result(FlutterError(code: "PTY_NOTIFY_CONFIG_FAILED", message: error.localizedDescription, details: nil))
                    }
                }
            }

        case "closePTY":
            let ptyId = i64("ptyId")
            let p = withLock { () -> PeershPTYSession? in
                sessionForPty.removeValue(forKey: ptyId)
                return ptys.removeValue(forKey: ptyId)
            }
            guard let p = p else { result(nil); return }
            worker.async {
                try? p.close()
                DispatchQueue.main.async { result(nil) }
            }

        case "getCwd":
            let ptyId = i64("ptyId")
            let s = withLock { () -> PeershSession? in
                guard let sid = sessionForPty[ptyId] else { return nil }
                return sessions[sid]
            }
            guard let s = s else { result(""); return }
            worker.async {
                let cwd = s.getCWD(ptyId)
                DispatchQueue.main.async { result(cwd) }
            }

        case "listSessionFiles":
            let ptyId = i64("ptyId")
            let path = str("path")
            let s = withLock { () -> PeershSession? in
                guard let sid = sessionForPty[ptyId] else { return nil }
                return sessions[sid]
            }
            guard let s = s else {
                result(FlutterError(code: "UNKNOWN_PTY", message: "no session for pty id=\(ptyId)", details: nil))
                return
            }
            worker.async {
                do {
                    let list = s.listSessionFiles(ptyId, path: path)
                    let total = Int(list?.len() ?? 0)
                    var items: [[String: Any]] = []
                    items.reserveCapacity(total)
                    for i in 0..<total {
                        guard let e = list?.get(i) else { continue }
                        items.append([
                            "name": e.name,
                            "path": e.path,
                            "isDir": e.isDir,
                            "size": e.size,
                            "modifiedUnixMs": e.modifiedUnixMs,
                        ])
                    }
                    DispatchQueue.main.async { result(items) }
                }
            }

        case "readSessionFile":
            let ptyId = i64("ptyId")
            let path = str("path")
            let maxBytes = i64("maxBytes")
            let s = withLock { () -> PeershSession? in
                guard let sid = sessionForPty[ptyId] else { return nil }
                return sessions[sid]
            }
            guard let s = s else {
                result(FlutterError(code: "UNKNOWN_PTY", message: "no session for pty id=\(ptyId)", details: nil))
                return
            }
            worker.async {
                // Swift imports gomobile's ReadSessionFile as readFile(_:path:maxBytes:).
                let fc = s.readFile(ptyId, path: path, maxBytes: maxBytes)
                let out: [String: Any] = [
                    "content": FlutterStandardTypedData(bytes: fc?.content ?? Data()),
                    "encoding": fc?.encoding ?? "",
                    "size": fc?.size ?? 0,
                    "truncated": fc?.truncated ?? false,
                    "error": fc?.error ?? "no response",
                ]
                DispatchQueue.main.async { result(out) }
            }

        default:
            result(FlutterMethodNotImplemented)
        }
    }

    // MARK: - Teardown

    /// Best-effort close of any sessions/ptys left open by the Dart side.
    /// Call from the app-delegate's terminate hook.
    func shutdown() {
        let (openPtys, openSessions): ([PeershPTYSession], [PeershSession]) = withLock {
            let p = Array(ptys.values); let s = Array(sessions.values)
            ptys.removeAll(); sessionForPty.removeAll(); sessions.removeAll()
            return (p, s)
        }
        for p in openPtys { try? p.close() }
        for s in openSessions { try? s.close() }
    }
}

// NOTE on the gomobile name collision: for a Go interface `Output`, gomobile
// emits BOTH an ObjC `@protocol PeershOutput` (what we implement) AND a
// same-named proxy `@interface PeershOutput` (used when Go returns one). A bare
// `: PeershOutput` in a class-inheritance clause resolves to the *class*, which
// is why conformance is declared in an `extension` (whose inheritance clause
// only admits protocols) below. For the same reason we never spell the protocol
// as a bare variable/parameter type — the buffering shim holds the forwarder as
// its concrete type instead.
//
// The protocol methods take `_Nullable` NSData/NSString, so the Swift
// requirements use optional `Data?` / `String?`.

// MARK: - Output forwarder (one-shot Exec streaming)

/// Forwards peersh.Output stream events to the EventChannel, tagged with the
/// session id so Dart can multiplex.
private final class SessionOutputForwarder: NSObject {
    private let sessionId: Int
    private weak var bridge: PeershBridge?

    init(sessionId: Int, bridge: PeershBridge) {
        self.sessionId = sessionId
        self.bridge = bridge
    }

    func forwardDone(_ errMessage: String) { forward("done", data: nil, error: errMessage) }

    fileprivate func forward(_ type: String, data: Data?, error: String) {
        var event: [String: Any] = ["sessionId": sessionId, "type": type]
        if let data = data { event["data"] = FlutterStandardTypedData(bytes: data) }
        if !error.isEmpty { event["error"] = error }
        bridge?.emit(event)
    }
}

// Swift's Clang importer disambiguates the class/protocol name collision by
// exposing the protocol with a `Protocol` suffix (same rule as NSObjectProtocol),
// so the gomobile `PeershOutput` protocol is `PeershOutputProtocol` here.
extension SessionOutputForwarder: PeershOutputProtocol {
    func onStdout(_ data: Data?) { forward("stdout", data: data, error: "") }
    func onStderr(_ data: Data?) { forward("stderr", data: data, error: "") }
    func onDone(_ errMessage: String?) { forward("done", data: nil, error: errMessage ?? "") }
}

// MARK: - PTY event forwarder

/// Forwards PTY output + exit events to the EventChannel, tagged with "type"
/// (ptyData / ptyExit) and "ptyId". Only ever called directly by the buffering
/// shim (never registered with Go), so it does not conform to PeershPTYHandler.
private final class PTYEventForwarder {
    private let ptyId: Int64
    private weak var bridge: PeershBridge?

    init(ptyId: Int64, bridge: PeershBridge) {
        self.ptyId = ptyId
        self.bridge = bridge
    }

    func onData(_ data: Data) {
        bridge?.emit([
            "ptyId": ptyId,
            "type": "ptyData",
            "data": FlutterStandardTypedData(bytes: data),
        ])
    }

    func onExit(_ exitCode: Int, _ errMessage: String) {
        var event: [String: Any] = ["ptyId": ptyId, "type": "ptyExit", "exitCode": exitCode]
        if !errMessage.isEmpty { event["error"] = errMessage }
        bridge?.emit(event)
    }
}

// MARK: - Reattach buffering shim

/// Temporary PeershPTYHandler that buffers data/exit events until the real
/// forwarder is available (the host-assigned ptyId is only known after openPTY
/// returns, but on reattach the host replays the scrollback ring buffer
/// immediately). Port of the Kotlin `tempHandler`.
private final class BufferingPTYHandler: NSObject {
    private let lock = NSLock()
    private var pendingData: [Data] = []
    private var pendingExits: [(Int, String)] = []
    private var real: PTYEventForwarder?

    func activate(_ rh: PTYEventForwarder) {
        lock.lock()
        let data = pendingData; pendingData.removeAll()
        let exits = pendingExits; pendingExits.removeAll()
        real = rh
        lock.unlock()
        for d in data { rh.onData(d) }
        for (code, msg) in exits { rh.onExit(code, msg) }
    }

    fileprivate func deliverData(_ data: Data) {
        var rh: PTYEventForwarder?
        lock.lock()
        rh = real
        if rh == nil { pendingData.append(data) }
        lock.unlock()
        rh?.onData(data)
    }

    fileprivate func deliverExit(_ exitCode: Int, _ msg: String) {
        var rh: PTYEventForwarder?
        lock.lock()
        rh = real
        if rh == nil { pendingExits.append((exitCode, msg)) }
        lock.unlock()
        rh?.onExit(exitCode, msg)
    }
}

extension BufferingPTYHandler: PeershPTYHandlerProtocol {
    func onData(_ data: Data?) { deliverData(data ?? Data()) }
    func onExit(_ exitCode: Int, errMessage: String?) { deliverExit(exitCode, errMessage ?? "") }
}
