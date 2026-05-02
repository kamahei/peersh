package dev.peersh.app

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder

// PeershForegroundService keeps the app process alive (and the QUIC
// keepalive flowing) while a peersh session is active. The service is
// started from MainActivity's "fgServiceStart" MethodChannel call when
// the terminal screen successfully connects, and stopped from
// "fgServiceStop" when the user backs out / disconnects.
//
// Android 14+ requires the foreground-service-type to be declared both
// in the manifest and at startForeground time; we use dataSync since
// the long-lived tunnel is closer to "background data exchange" than
// to mediaPlayback / location.
class PeershForegroundService : Service() {
    companion object {
        const val CHANNEL_ID = "peersh.session"
        const val NOTIFICATION_ID = 0xE5

        const val ACTION_START = "dev.peersh.fg.START"
        const val ACTION_STOP = "dev.peersh.fg.STOP"
        const val EXTRA_TITLE = "title"
        const val EXTRA_BODY = "body"

        fun start(context: Context, title: String, body: String) {
            val i = Intent(context, PeershForegroundService::class.java).apply {
                action = ACTION_START
                putExtra(EXTRA_TITLE, title)
                putExtra(EXTRA_BODY, body)
            }
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                context.startForegroundService(i)
            } else {
                context.startService(i)
            }
        }

        fun stop(context: Context) {
            val i = Intent(context, PeershForegroundService::class.java).apply {
                action = ACTION_STOP
            }
            context.startService(i)
        }
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> {
                stopForeground(STOP_FOREGROUND_REMOVE)
                stopSelf()
                return START_NOT_STICKY
            }
            else -> {
                ensureChannel()
                val title = intent?.getStringExtra(EXTRA_TITLE) ?: "peersh"
                val body = intent?.getStringExtra(EXTRA_BODY) ?: "Session active"
                val notif = buildNotification(title, body)
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
                    startForeground(
                        NOTIFICATION_ID,
                        notif,
                        ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC,
                    )
                } else {
                    startForeground(NOTIFICATION_ID, notif)
                }
            }
        }
        // START_NOT_STICKY: if the OS kills us due to memory pressure,
        // don't auto-restart. The terminal screen restarts the service
        // on its own when it reconnects.
        return START_NOT_STICKY
    }

    private fun ensureChannel() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        val nm = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        if (nm.getNotificationChannel(CHANNEL_ID) != null) return
        val ch = NotificationChannel(
            CHANNEL_ID,
            "peersh session",
            NotificationManager.IMPORTANCE_LOW,
        ).apply {
            description = "Keeps the QUIC connection alive while you're connected to a PC."
            setShowBadge(false)
        }
        nm.createNotificationChannel(ch)
    }

    private fun buildNotification(title: String, body: String): Notification {
        val openIntent = Intent(this, MainActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_REORDER_TO_FRONT
        }
        val contentPI = PendingIntent.getActivity(
            this,
            0,
            openIntent,
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        val builder = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            Notification.Builder(this, CHANNEL_ID)
        } else {
            @Suppress("DEPRECATION") Notification.Builder(this)
        }
        return builder
            .setContentTitle(title)
            .setContentText(body)
            .setSmallIcon(android.R.drawable.stat_sys_data_bluetooth) // placeholder; ic_stat is a Phase 7 polish item
            .setContentIntent(contentPI)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .build()
    }
}
