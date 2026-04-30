package dev.peersh.app

import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.MethodChannel
import peersh.Peersh

class MainActivity : FlutterActivity() {
    private val channelName = "dev.peersh/bridge"

    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        MethodChannel(flutterEngine.dartExecutor.binaryMessenger, channelName)
            .setMethodCallHandler { call, result ->
                try {
                    when (call.method) {
                        "version" -> result.success(Peersh.version())
                        "echo" -> {
                            val addr = call.argument<String>("addr") ?: ""
                            val cmd = call.argument<String>("command") ?: ""
                            // Go-side Echo blocks; run on a worker thread so
                            // the UI thread stays responsive during the
                            // QUIC dial + handshake.
                            Thread {
                                val out = Peersh.echo(addr, cmd)
                                runOnUiThread { result.success(out) }
                            }.start()
                        }
                        else -> result.notImplemented()
                    }
                } catch (t: Throwable) {
                    result.error("BRIDGE_ERROR", t.message ?: t.javaClass.simpleName, null)
                }
            }
    }
}
