package dev.peersh.app

import android.os.Handler
import android.os.Looper
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.EventChannel
import io.flutter.plugin.common.MethodChannel
import peersh.Output
import peersh.Peersh
import peersh.Session
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicInteger

class MainActivity : FlutterActivity() {
    private val controlChannelName = "dev.peersh/bridge"
    private val eventChannelName = "dev.peersh/session/events"

    private val sessions = ConcurrentHashMap<Int, Session>()
    private val nextSessionId = AtomicInteger(1)
    private val executor = Executors.newCachedThreadPool()
    private val mainHandler = Handler(Looper.getMainLooper())

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
                                val s = Peersh.openDirectSession(addr)
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
                        executor.submit {
                            try {
                                val s = Peersh.openSignalingSession(signaling, user, psk, target, stun)
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
                    else -> result.notImplemented()
                }
            } catch (t: Throwable) {
                result.error("BRIDGE_ERROR", t.message ?: t.javaClass.simpleName, null)
            }
        }
    }

    override fun onDestroy() {
        // Best-effort: close any sessions left open by the Dart side.
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
}
