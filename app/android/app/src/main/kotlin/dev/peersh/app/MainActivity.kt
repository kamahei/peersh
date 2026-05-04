package dev.peersh.app

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import androidx.core.app.ActivityCompat
import androidx.core.app.NotificationManagerCompat
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.EventChannel
import io.flutter.plugin.common.MethodChannel
import peersh.Output
import peersh.PTYHandler
import peersh.PTYSession
import peersh.Peersh
import peersh.Session
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicInteger

class MainActivity : FlutterActivity() {
    private val controlChannelName = "dev.peersh/bridge"
    private val eventChannelName = "dev.peersh/session/events"

    companion object {
        private const val REQ_POST_NOTIFICATIONS = 0x70
    }

    private val sessions = ConcurrentHashMap<Int, Session>()
    // PTY map keyed by host-assigned PTYSession.id() (Long): the same id
    // doubles as the file-API handle, so storing it directly keeps the
    // platform side and the host side in sync without a separate lookup.
    private val ptys = ConcurrentHashMap<Long, PTYSession>()
    // sessionForPty maps PTY id -> session id, so the file-API methods
    // can resolve the QUIC connection from a single ptyId arg.
    private val sessionForPty = ConcurrentHashMap<Long, Int>()
    private val nextSessionId = AtomicInteger(1)
    private val executor = Executors.newCachedThreadPool()
    private val mainHandler = Handler(Looper.getMainLooper())

    // Persistent ed25519 key directory. mobile-core stores the device's
    // long-lived keypair here; reusing the same directory across dials
    // keeps the mTLS-derived device_id stable, which is what lets the
    // host's ptyhost.Manager reattach a client to its earlier shells
    // after a QUIC reconnect (the Manager partitions PTYs by Owner =
    // peer device_id). noBackupFilesDir is used so the key never syncs
    // to other devices via Auto Backup; that would be both a privacy
    // leak and a correctness bug, since each device must have its own
    // identity.
    private val deviceKeyDir: String by lazy {
        java.io.File(applicationContext.noBackupFilesDir, "peersh-device-key").apply {
            if (!exists()) mkdirs()
        }.absolutePath
    }

    @Volatile private var sink: EventChannel.EventSink? = null

    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        val messenger = flutterEngine.dartExecutor.binaryMessenger

        EventChannel(messenger, eventChannelName).setStreamHandler(
            object : EventChannel.StreamHandler {
                override fun onListen(arguments: Any?, events: EventChannel.EventSink?) {
                    sink = events
                }
                override fun onCancel(arguments: Any?) {
                    sink = null
                }
            }
        )

        MethodChannel(messenger, controlChannelName).setMethodCallHandler { call, result ->
            try {
                when (call.method) {
                    "version" -> result.success(Peersh.version())
                    "echo" -> {
                        val addr = call.argument<String>("addr") ?: ""
                        val cmd = call.argument<String>("command") ?: ""
                        executor.submit {
                            val out = Peersh.echo(addr, cmd)
                            mainHandler.post { result.success(out) }
                        }
                    }
                    "openDirectSession" -> {
                        val addr = call.argument<String>("addr") ?: ""
                        executor.submit {
                            try {
                                val s = Peersh.openDirectSessionWithKey(addr, deviceKeyDir)
                                val id = nextSessionId.getAndIncrement()
                                sessions[id] = s
                                mainHandler.post { result.success(id) }
                            } catch (t: Throwable) {
                                mainHandler.post {
                                    result.error("OPEN_FAILED", t.message ?: t.javaClass.simpleName, null)
                                }
                            }
                        }
                    }
                    "openSignalingSession" -> {
                        val signaling = call.argument<String>("signaling") ?: ""
                        val user = call.argument<String>("user") ?: ""
                        val psk = call.argument<String>("psk") ?: ""
                        val target = call.argument<String>("target") ?: ""
                        val stun = call.argument<String>("stun") ?: ""
                        val idleTimeoutSec = (call.argument<Number>("idleTimeoutSec") ?: 0).toInt()
                        executor.submit {
                            try {
                                val s = Peersh.openSignalingSessionV2(
                                    signaling, user, psk, target, stun, deviceKeyDir, idleTimeoutSec)
                                val id = nextSessionId.getAndIncrement()
                                sessions[id] = s
                                mainHandler.post { result.success(id) }
                            } catch (t: Throwable) {
                                mainHandler.post {
                                    result.error("OPEN_FAILED", t.message ?: t.javaClass.simpleName, null)
                                }
                            }
                        }
                    }
                    "openFirebaseSignalingSession" -> {
                        val signaling = call.argument<String>("signaling") ?: ""
                        val idToken = call.argument<String>("idToken") ?: ""
                        val appCheckToken = call.argument<String>("appCheckToken") ?: ""
                        val target = call.argument<String>("target") ?: ""
                        val stun = call.argument<String>("stun") ?: ""
                        val idleTimeoutSec = (call.argument<Number>("idleTimeoutSec") ?: 0).toInt()
                        executor.submit {
                            try {
                                val s = Peersh.openFirebaseSignalingSessionV2(
                                    signaling, idToken, appCheckToken, target, stun, deviceKeyDir, idleTimeoutSec)
                                val id = nextSessionId.getAndIncrement()
                                sessions[id] = s
                                mainHandler.post { result.success(id) }
                            } catch (t: Throwable) {
                                mainHandler.post {
                                    result.error("OPEN_FAILED", t.message ?: t.javaClass.simpleName, null)
                                }
                            }
                        }
                    }
                    "exec" -> {
                        val sessionId = (call.argument<Number>("sessionId") ?: 0).toInt()
                        val command = call.argument<String>("command") ?: ""
                        val s = sessions[sessionId]
                        if (s == null) {
                            result.error("UNKNOWN_SESSION", "no session for id=$sessionId", null)
                            return@setMethodCallHandler
                        }
                        executor.submit {
                            val handler = SessionEventOutput(sessionId) { sink }
                            try {
                                s.exec(command, handler)
                            } catch (t: Throwable) {
                                // Defensive: Go-side returns errors via OnDone, but
                                // any unexpected throw lands here.
                                handler.forwardDone(t.message ?: t.javaClass.simpleName)
                            }
                            mainHandler.post { result.success(null) }
                        }
                    }
                    "readFile" -> {
                        val sessionId = (call.argument<Number>("sessionId") ?: 0).toInt()
                        val path = call.argument<String>("path") ?: ""
                        val s = sessions[sessionId]
                        if (s == null) {
                            result.error("UNKNOWN_SESSION", "no session for id=$sessionId", null)
                            return@setMethodCallHandler
                        }
                        executor.submit {
                            val out = s.readFile(path)
                            mainHandler.post { result.success(out) }
                        }
                    }
                    "closeSession" -> {
                        val sessionId = (call.argument<Number>("sessionId") ?: 0).toInt()
                        val s = sessions.remove(sessionId)
                        if (s == null) {
                            result.success(null)
                            return@setMethodCallHandler
                        }
                        executor.submit {
                            try { s.close() } catch (_: Throwable) {}
                            mainHandler.post { result.success(null) }
                        }
                    }
                    "fgServiceStart" -> {
                        val title = call.argument<String>("title") ?: "peersh"
                        val body = call.argument<String>("body") ?: "Session active"
                        PeershForegroundService.start(applicationContext, title, body)
                        result.success(null)
                    }
                    "fgServiceStop" -> {
                        PeershForegroundService.stop(applicationContext)
                        result.success(null)
                    }
                    "notificationsEnabled" -> {
                        val ok = NotificationManagerCompat.from(applicationContext)
                            .areNotificationsEnabled()
                        result.success(ok)
                    }
                    "requestNotifications" -> {
                        // Always (re)create the command_ready channel — it is
                        // idempotent and harmless to call repeatedly. Doing it
                        // here means the Dart bridge call also primes the
                        // channel without needing a separate platform method.
                        ensureCommandReadyChannel()

                        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
                            val granted = ActivityCompat.checkSelfPermission(
                                this,
                                Manifest.permission.POST_NOTIFICATIONS,
                            ) == PackageManager.PERMISSION_GRANTED
                            if (!granted) {
                                ActivityCompat.requestPermissions(
                                    this,
                                    arrayOf(Manifest.permission.POST_NOTIFICATIONS),
                                    REQ_POST_NOTIFICATIONS,
                                )
                                // We don't await the user's response here;
                                // the caller polls notificationsEnabled or
                                // re-prompts on next session start.
                            }
                            result.success(granted)
                        } else {
                            result.success(true)
                        }
                    }
                    "openNotificationSettings" -> {
                        val intent = Intent().apply {
                            action = Settings.ACTION_APP_NOTIFICATION_SETTINGS
                            putExtra(Settings.EXTRA_APP_PACKAGE, packageName)
                            flags = Intent.FLAG_ACTIVITY_NEW_TASK
                        }
                        try {
                            startActivity(intent)
                        } catch (_: Throwable) {
                            // Older Android: fall back to app settings
                            val fallback = Intent(
                                Settings.ACTION_APPLICATION_DETAILS_SETTINGS,
                                Uri.fromParts("package", packageName, null),
                            ).apply { flags = Intent.FLAG_ACTIVITY_NEW_TASK }
                            startActivity(fallback)
                        }
                        result.success(null)
                    }
                    "openPTY" -> {
                        val sessionId = (call.argument<Number>("sessionId") ?: 0).toInt()
                        val command = call.argument<String>("command") ?: ""
                        val cols = (call.argument<Number>("cols") ?: 80).toInt()
                        val rows = (call.argument<Number>("rows") ?: 24).toInt()
                        val reattachHandle = call.argument<String>("reattachHandle") ?: ""
                        val s = sessions[sessionId]
                        if (s == null) {
                            result.error("UNKNOWN_SESSION", "no session for id=$sessionId", null)
                            return@setMethodCallHandler
                        }
                        executor.submit {
                            try {
                                // Buffering shim: openPTY[Reattach] needs a
                                // PTYHandler before we have the host-assigned
                                // ptyId (only known after the call returns),
                                // but on reattach the host starts streaming
                                // the scrollback ring buffer the moment the
                                // ack lands. Without this buffer those
                                // replay bytes would arrive while
                                // realHandler is still null and be silently
                                // dropped — which is why a freshly
                                // reconnected client used to render a blank
                                // terminal until new output arrived.
                                val tempHandler = object : PTYHandler {
                                    private val lock = Any()
                                    private val pendingData = mutableListOf<ByteArray>()
                                    private val pendingExits = mutableListOf<Pair<Long, String>>()
                                    @Volatile var realHandler: PTYEventHandler? = null

                                    override fun onData(data: ByteArray) {
                                        var rh: PTYEventHandler? = null
                                        synchronized(lock) {
                                            rh = realHandler
                                            if (rh == null) pendingData.add(data)
                                        }
                                        rh?.onData(data)
                                    }

                                    override fun onExit(exitCode: Long, errMessage: String) {
                                        var rh: PTYEventHandler? = null
                                        synchronized(lock) {
                                            rh = realHandler
                                            if (rh == null) pendingExits.add(exitCode to errMessage)
                                        }
                                        rh?.onExit(exitCode, errMessage)
                                    }

                                    fun activate(rh: PTYEventHandler) {
                                        synchronized(lock) {
                                            for (d in pendingData) rh.onData(d)
                                            pendingData.clear()
                                            for ((c, m) in pendingExits) rh.onExit(c, m)
                                            pendingExits.clear()
                                            realHandler = rh
                                        }
                                    }
                                }
                                val p = if (reattachHandle.isNotEmpty()) {
                                    s.openPTYReattach(reattachHandle, cols.toLong(), rows.toLong(), tempHandler)
                                } else {
                                    s.openPTY(command, cols.toLong(), rows.toLong(), tempHandler)
                                }
                                val ptyId = p.id()
                                tempHandler.activate(PTYEventHandler(ptyId) { sink })
                                ptys[ptyId] = p
                                sessionForPty[ptyId] = sessionId
                                // Best-effort: poll the host-assigned
                                // reattach handle for the next 2 s so
                                // the Dart side can persist it. The ack
                                // arrives within a few hundred ms in
                                // practice.
                                var handle = ""
                                for (i in 0 until 20) {
                                    handle = p.handle()
                                    if (handle.isNotEmpty()) break
                                    Thread.sleep(100)
                                }
                                mainHandler.post {
                                    result.success(hashMapOf<String, Any?>(
                                        "ptyId" to ptyId,
                                        "handle" to handle,
                                    ))
                                }
                            } catch (t: Throwable) {
                                mainHandler.post {
                                    result.error("PTY_OPEN_FAILED", t.message ?: t.javaClass.simpleName, null)
                                }
                            }
                        }
                    }
                    "listPTYs" -> {
                        val sessionId = (call.argument<Number>("sessionId") ?: 0).toInt()
                        val s = sessions[sessionId]
                        if (s == null) {
                            result.error("UNKNOWN_SESSION", "no session for id=$sessionId", null)
                            return@setMethodCallHandler
                        }
                        executor.submit {
                            try {
                                val list = s.listPTYs()
                                val total = list.len().toInt()
                                val items = ArrayList<HashMap<String, Any?>>(total)
                                for (i in 0 until total) {
                                    val e = list.get(i.toLong()) ?: continue
                                    items.add(hashMapOf<String, Any?>(
                                        "handle" to e.handle,
                                        "command" to e.command,
                                        "attached" to e.attached,
                                        "attachedCount" to e.attachedCount.toInt(),
                                        "cwd" to e.cwd,
                                        "lastSeenUnixMs" to e.lastSeenUnixMs,
                                    ))
                                }
                                mainHandler.post { result.success(items) }
                            } catch (t: Throwable) {
                                mainHandler.post {
                                    result.error("LIST_PTYS_FAILED", t.message ?: t.javaClass.simpleName, null)
                                }
                            }
                        }
                    }
                    "killPTY" -> {
                        val sessionId = (call.argument<Number>("sessionId") ?: 0).toInt()
                        val handle = call.argument<String>("handle") ?: ""
                        val s = sessions[sessionId]
                        if (s == null) {
                            result.error("UNKNOWN_SESSION", "no session for id=$sessionId", null)
                            return@setMethodCallHandler
                        }
                        executor.submit {
                            try {
                                val err = s.killPTY(handle)
                                mainHandler.post { result.success(err) }
                            } catch (t: Throwable) {
                                mainHandler.post {
                                    result.error("KILL_PTY_FAILED", t.message ?: t.javaClass.simpleName, null)
                                }
                            }
                        }
                    }
                    "ptyInput" -> {
                        val ptyId = (call.argument<Number>("ptyId") ?: 0).toLong()
                        val data = call.argument<ByteArray>("data") ?: ByteArray(0)
                        val p = ptys[ptyId]
                        if (p == null) {
                            result.error("UNKNOWN_PTY", "no pty for id=$ptyId", null)
                            return@setMethodCallHandler
                        }
                        executor.submit {
                            try {
                                p.write(data)
                                mainHandler.post { result.success(null) }
                            } catch (t: Throwable) {
                                mainHandler.post {
                                    result.error("PTY_WRITE_FAILED", t.message ?: t.javaClass.simpleName, null)
                                }
                            }
                        }
                    }
                    "ptyResize" -> {
                        val ptyId = (call.argument<Number>("ptyId") ?: 0).toLong()
                        val cols = (call.argument<Number>("cols") ?: 80).toInt()
                        val rows = (call.argument<Number>("rows") ?: 24).toInt()
                        val p = ptys[ptyId]
                        if (p == null) {
                            result.error("UNKNOWN_PTY", "no pty for id=$ptyId", null)
                            return@setMethodCallHandler
                        }
                        executor.submit {
                            try {
                                p.resize(cols.toLong(), rows.toLong())
                                mainHandler.post { result.success(null) }
                            } catch (t: Throwable) {
                                mainHandler.post {
                                    result.error("PTY_RESIZE_FAILED", t.message ?: t.javaClass.simpleName, null)
                                }
                            }
                        }
                    }
                    "ptyNotificationConfig" -> {
                        val ptyId = (call.argument<Number>("ptyId") ?: 0).toLong()
                        val enabled = call.argument<Boolean>("enabled") ?: false
                        val threshold = (call.argument<Number>("thresholdSeconds") ?: 0).toInt()
                        val idle = (call.argument<Number>("idleSeconds") ?: 0).toInt()
                        val tabLabel = call.argument<String>("tabLabel") ?: ""
                        val mobileDeviceId = call.argument<String>("mobileDeviceId") ?: ""
                        val p = ptys[ptyId]
                        if (p == null) {
                            result.error("UNKNOWN_PTY", "no pty for id=$ptyId", null)
                            return@setMethodCallHandler
                        }
                        executor.submit {
                            try {
                                p.sendNotificationConfig(enabled, threshold.toLong(), idle.toLong(), tabLabel, mobileDeviceId)
                                mainHandler.post { result.success(null) }
                            } catch (t: Throwable) {
                                mainHandler.post {
                                    result.error("PTY_NOTIFY_CONFIG_FAILED", t.message ?: t.javaClass.simpleName, null)
                                }
                            }
                        }
                    }
                    "closePTY" -> {
                        val ptyId = (call.argument<Number>("ptyId") ?: 0).toLong()
                        val p = ptys.remove(ptyId)
                        sessionForPty.remove(ptyId)
                        if (p == null) {
                            result.success(null)
                            return@setMethodCallHandler
                        }
                        executor.submit {
                            try { p.close() } catch (_: Throwable) {}
                            mainHandler.post { result.success(null) }
                        }
                    }
                    "getCwd" -> {
                        val ptyId = (call.argument<Number>("ptyId") ?: 0).toLong()
                        val sessionId = sessionForPty[ptyId]
                        val s = sessionId?.let { sessions[it] }
                        if (s == null) {
                            result.success("")
                            return@setMethodCallHandler
                        }
                        executor.submit {
                            val cwd = try { s.getCWD(ptyId) } catch (_: Throwable) { "" }
                            mainHandler.post { result.success(cwd) }
                        }
                    }
                    "listSessionFiles" -> {
                        val ptyId = (call.argument<Number>("ptyId") ?: 0).toLong()
                        val path = call.argument<String>("path") ?: ""
                        val sessionId = sessionForPty[ptyId]
                        val s = sessionId?.let { sessions[it] }
                        if (s == null) {
                            result.error("UNKNOWN_PTY", "no session for pty id=$ptyId", null)
                            return@setMethodCallHandler
                        }
                        executor.submit {
                            try {
                                val list = s.listSessionFiles(ptyId, path)
                                val total = list.len().toInt()
                                val items = ArrayList<HashMap<String, Any?>>(total)
                                for (i in 0 until total) {
                                    val e = list.get(i.toLong()) ?: continue
                                    items.add(hashMapOf<String, Any?>(
                                        "name" to e.name,
                                        "path" to e.path,
                                        "isDir" to e.isDir,
                                        "size" to e.size,
                                        "modifiedUnixMs" to e.modifiedUnixMs,
                                    ))
                                }
                                mainHandler.post { result.success(items) }
                            } catch (t: Throwable) {
                                mainHandler.post {
                                    result.error("LIST_FAILED", t.message ?: t.javaClass.simpleName, null)
                                }
                            }
                        }
                    }
                    "readSessionFile" -> {
                        val ptyId = (call.argument<Number>("ptyId") ?: 0).toLong()
                        val path = call.argument<String>("path") ?: ""
                        val maxBytes = (call.argument<Number>("maxBytes") ?: 0).toLong()
                        val sessionId = sessionForPty[ptyId]
                        val s = sessionId?.let { sessions[it] }
                        if (s == null) {
                            result.error("UNKNOWN_PTY", "no session for pty id=$ptyId", null)
                            return@setMethodCallHandler
                        }
                        executor.submit {
                            try {
                                val fc = s.readSessionFile(ptyId, path, maxBytes)
                                val out = hashMapOf<String, Any?>(
                                    "content" to fc.content,
                                    "encoding" to fc.encoding,
                                    "size" to fc.size,
                                    "truncated" to fc.truncated,
                                    "error" to fc.error,
                                )
                                mainHandler.post { result.success(out) }
                            } catch (t: Throwable) {
                                mainHandler.post {
                                    result.error("READ_FAILED", t.message ?: t.javaClass.simpleName, null)
                                }
                            }
                        }
                    }
                    else -> result.notImplemented()
                }
            } catch (t: Throwable) {
                result.error("BRIDGE_ERROR", t.message ?: t.javaClass.simpleName, null)
            }
        }
    }

    override fun onDestroy() {
        // Best-effort: close any sessions/ptys left open by the Dart side.
        for ((_, p) in ptys) {
            try { p.close() } catch (_: Throwable) {}
        }
        ptys.clear()
        for ((_, s) in sessions) {
            try { s.close() } catch (_: Throwable) {}
        }
        sessions.clear()
        executor.shutdownNow()
        super.onDestroy()
    }

    /**
     * SessionEventOutput implements peersh.Output and forwards stream
     * events to the Flutter EventChannel. The events are tagged with the
     * session id so Dart can multiplex if multiple sessions are active.
     */
    private inner class SessionEventOutput(
        private val sessionId: Int,
        private val sinkRef: () -> EventChannel.EventSink?,
    ) : Output {
        override fun onStdout(data: ByteArray) {
            forward("stdout", data, "")
        }
        override fun onStderr(data: ByteArray) {
            forward("stderr", data, "")
        }
        override fun onDone(errMessage: String) {
            forward("done", null, errMessage)
        }

        fun forwardDone(errMessage: String) {
            forward("done", null, errMessage)
        }

        private fun forward(type: String, data: ByteArray?, error: String) {
            val event = HashMap<String, Any?>().apply {
                put("sessionId", sessionId)
                put("type", type)
                if (data != null) put("data", data) // platform codec sends ByteArray as Uint8List
                if (error.isNotEmpty()) put("error", error)
            }
            mainHandler.post { sinkRef()?.success(event) }
        }
    }

    /**
     * Idempotently creates the v2-B "command_ready" notification
     * channel. The Cloud Function `onNotificationCreated` sets this
     * id on every dispatched FCM message so the OS picks the right
     * importance / sound. Safe to call from any caller; no-op on
     * Android < O.
     */
    private fun ensureCommandReadyChannel() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        val nm = getSystemService(android.app.NotificationManager::class.java) ?: return
        if (nm.getNotificationChannel("command_ready") != null) return
        val ch = android.app.NotificationChannel(
            "command_ready",
            "Command ready",
            android.app.NotificationManager.IMPORTANCE_HIGH,
        ).apply {
            description = "Notifies you when a long-running command finishes on a connected PC."
            enableLights(true)
            enableVibration(true)
        }
        nm.createNotificationChannel(ch)
    }

    /**
     * PTYEventHandler implements peersh.PTYHandler and forwards PTY output
     * + exit events to the Flutter EventChannel. Events are tagged with a
     * "type" key so Dart can multiplex (ptyData / ptyExit) and a "ptyId"
     * key so the UI can correlate to a specific terminal screen.
     */
    private inner class PTYEventHandler(
        private val ptyId: Long,
        private val sinkRef: () -> EventChannel.EventSink?,
    ) : PTYHandler {
        override fun onData(data: ByteArray) {
            val event = HashMap<String, Any?>().apply {
                put("ptyId", ptyId)
                put("type", "ptyData")
                put("data", data)
            }
            mainHandler.post { sinkRef()?.success(event) }
        }
        override fun onExit(exitCode: Long, errMessage: String) {
            val event = HashMap<String, Any?>().apply {
                put("ptyId", ptyId)
                put("type", "ptyExit")
                put("exitCode", exitCode)
                if (errMessage.isNotEmpty()) put("error", errMessage)
            }
            mainHandler.post { sinkRef()?.success(event) }
        }
    }
}
