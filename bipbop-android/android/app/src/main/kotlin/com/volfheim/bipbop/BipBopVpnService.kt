package com.volfheim.bipbop

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.Service
import android.content.Intent
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import android.util.Log
import androidx.localbroadcastmanager.content.LocalBroadcastManager
import kotlinx.coroutines.*
import java.io.*

class BipBopVpnService : VpnService() {

    init {
        System.loadLibrary("bipbop")
    }

    private external fun startVpnNative(smartKey: String, tunFd: Int, mtu: Int, dns: String): Int
    private external fun reconnectVpnNative()
    private external fun stopVpnNative()

    private lateinit var connectivityManager: android.net.ConnectivityManager

    companion object {
        const val ACTION_CONNECT = "com.volfheim.bipbop.START"
        const val ACTION_STOP = "com.volfheim.bipbop.STOP"
        const val ACTION_VPN_STATE_CHANGED = "com.volfheim.bipbop.STATE_CHANGED"
        const val ACTION_LOG = "com.volfheim.bipbop.LOG"
        private const val TAG = "BipBopVPN"
        private const val NOTIF_ID = 1001
        private const val CHANNEL_ID = "vpn_channel"
    }

    private var vpnInterface: ParcelFileDescriptor? = null
    private var vpnScope = CoroutineScope(Dispatchers.IO + SupervisorJob())

    @androidx.annotation.Keep
    fun onNativeLog(level: String, msg: String) {
        Log.d(TAG, "[$level] $msg")
        LocalBroadcastManager.getInstance(this).sendBroadcast(Intent(ACTION_LOG).putExtra("log", "[$level] $msg"))
    }

    @androidx.annotation.Keep
    fun onVpnStatusChanged(status: String) {
        Log.i(TAG, "Native status: $status")
        broadcastState(status)
    }

    override fun onCreate() {
        super.onCreate()
        connectivityManager = getSystemService(android.content.Context.CONNECTIVITY_SERVICE) as android.net.ConnectivityManager
    }

    override fun onDestroy() {
        super.onDestroy()
        vpnScope.cancel()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val action = intent?.action
        if (action == ACTION_CONNECT) {
            showNotification("Запуск...")
        }
        
        when (action) {
            ACTION_CONNECT -> {
                val smartKey = intent.getStringExtra("EXTRA_SMART_KEY")
                val proxyOnly = intent.getBooleanExtra("EXTRA_PROXY_ONLY", false)
                if (smartKey != null) {
                    establishVpn(smartKey, proxyOnly)
                } else {
                    broadcastState("error")
                    stopSelf()
                }
            }
            ACTION_STOP -> {
                shutdown()
            }
        }
        return START_STICKY
    }

    private fun establishVpn(smartKey: String, proxyOnly: Boolean) {
        vpnScope.launch {
            broadcastState("connecting")
            
            if (proxyOnly) {
                updateNotification("Включен SOCKS5 прокси")
                broadcastState("connected")
                Log.i(TAG, "Starting native Go core in Proxy-Only mode (fd = -1)")
                runCatching {
                    val result = startVpnNative(smartKey, -1, 1200, "77.88.8.8")
                    if (result != 0) {
                        Log.e(TAG, "Native core failed: $result")
                        shutdown()
                    }
                }.onFailure { e ->
                    Log.e(TAG, "JNI Error: ${e.message}")
                    shutdown()
                }
                return@launch
            }

            val builder = Builder()
                .setMtu(1280)
                .addAddress("10.0.0.2", 24)
                .addDnsServer("77.88.8.8")
                .setSession("bip-bop VPN")

            val publicRoutes = arrayOf(
                "1.0.0.0/8", "2.0.0.0/7", "4.0.0.0/6", "8.0.0.0/7", "11.0.0.0/8",
                "12.0.0.0/6", "16.0.0.0/4", "32.0.0.0/3", "64.0.0.0/2", "128.0.0.0/3",
                "160.0.0.0/5", "168.0.0.0/6", "172.0.0.0/12", "172.32.0.0/11",
                "172.64.0.0/10", "172.128.0.0/9", "173.0.0.0/8", "174.0.0.0/7",
                "176.0.0.0/4", "192.0.0.0/9", "192.128.0.0/11", "192.160.0.0/13",
                "192.169.0.0/16", "192.170.0.0/15", "192.172.0.0/14", "192.176.0.0/12",
                "192.192.0.0/10", "193.0.0.0/8", "194.0.0.0/7", "196.0.0.0/6",
                "200.0.0.0/5", "208.0.0.0/4", "224.0.0.0/3"
            )
            for (route in publicRoutes) {
                val parts = route.split("/")
                builder.addRoute(parts[0], parts[1].toInt())
            }

            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                try {
                    builder.addDisallowedApplication(packageName)
                } catch (e: Exception) {
                    Log.e(TAG, "Failed to exclude app: ${e.message}")
                }
            }

            vpnInterface = builder.establish()

            if (vpnInterface != null) {
                val fd = vpnInterface!!.fd
                updateNotification("Вы защищены")
                broadcastState("connected")
                
                Log.i(TAG, "Starting native Go core with fd=$fd")
                runCatching {
                    val result = startVpnNative(smartKey, fd, 1200, "77.88.8.8")
                    if (result != 0) {
                        Log.e(TAG, "Native core failed to start: $result")
                        shutdown()
                    }
                }.onFailure { e ->
                    Log.e(TAG, "JNI Error: ${e.message}")
                    shutdown()
                }
            } else {
                Log.e(TAG, "Failed to establish VPN interface")
                broadcastState("error")
                stopSelf()
            }
        }
    }

    private fun showNotification(content: String) {
        val notification = createNotification(content)
        startForeground(NOTIF_ID, notification)
    }

    private fun updateNotification(content: String) {
        val manager = getSystemService(NOTIFICATION_SERVICE) as NotificationManager
        val notification = createNotification(content)
        manager.notify(NOTIF_ID, notification)
    }

    private fun createNotification(content: String): Notification {
        val manager = getSystemService(NOTIFICATION_SERVICE) as NotificationManager
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val channel = NotificationChannel(CHANNEL_ID, "VPN Status", NotificationManager.IMPORTANCE_LOW)
            manager.createNotificationChannel(channel)
        }

        return if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            Notification.Builder(this, CHANNEL_ID)
                .setContentTitle("BipBop VPN")
                .setContentText(content)
                .setSmallIcon(android.R.drawable.ic_dialog_info)
                .build()
        } else {
            Notification.Builder(this)
                .setContentTitle("BipBop VPN")
                .setContentText(content)
                .setSmallIcon(android.R.drawable.ic_dialog_info)
                .setPriority(Notification.PRIORITY_LOW)
                .build()
        }
    }

    private fun broadcastState(state: String) {
        val intent = Intent(ACTION_VPN_STATE_CHANGED).putExtra("status", state)
        LocalBroadcastManager.getInstance(this).sendBroadcast(intent)
    }

    private fun shutdown() {
        vpnScope.launch {
            Log.i(TAG, "Shutting down VPN service...")
            
            // Мгновенно убираем иконку VPN
            runCatching {
                vpnInterface?.close()
                vpnInterface = null
            }
            
            withContext(Dispatchers.Main) {
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.N) {
                    stopForeground(STOP_FOREGROUND_REMOVE)
                } else {
                    stopForeground(true)
                }
            }
            broadcastState("disconnected")
            
            // Завершаем Go-кора (может блокировать) в фоне
            runCatching { stopVpnNative() }
            
            withContext(Dispatchers.Main) {
                stopSelf()
            }
        }
    }
}
