package com.volfheim.bipbop

import android.app.Activity
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.net.VpnService
import android.os.Bundle
import androidx.localbroadcastmanager.content.LocalBroadcastManager
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.MethodChannel

class MainActivity: FlutterActivity() {
    private val CHANNEL = "com.volfheim.bipbop/vpn"
    private var methodChannel: MethodChannel? = null
    private var pendingKey: String? = null
    private var pendingProxyOnly: Boolean = false
    private var pendingUpstream: String = ""
    private val REQ_VPN_PREPARE = 0x1

    private val vpnReceiver = object : BroadcastReceiver() {
        override fun onReceive(context: Context?, intent: Intent?) {
            when (intent?.action) {
                BipBopVpnService.ACTION_LOG -> {
                    val log = intent.getStringExtra("log")
                    log?.let { methodChannel?.invokeMethod("onLog", it) }
                }
                BipBopVpnService.ACTION_VPN_STATE_CHANGED -> {
                    val status = intent.getStringExtra("status")
                    status?.let { methodChannel?.invokeMethod("onStateChanged", it) }
                }
            }
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val filter = IntentFilter().apply {
            addAction(BipBopVpnService.ACTION_LOG)
            addAction(BipBopVpnService.ACTION_VPN_STATE_CHANGED)
        }
        LocalBroadcastManager.getInstance(this).registerReceiver(vpnReceiver, filter)
    }

    override fun onDestroy() {
        LocalBroadcastManager.getInstance(this).unregisterReceiver(vpnReceiver)
        super.onDestroy()
    }

    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        methodChannel = MethodChannel(flutterEngine.dartExecutor.binaryMessenger, CHANNEL)
        methodChannel?.setMethodCallHandler { call, result ->
            when (call.method) {
                "startVpn" -> {
                    val key = call.argument<String>("smartKey")
                    val proxyOnly = call.argument<Boolean>("proxyOnly") ?: false
                    val upstreamProxy = call.argument<String>("upstreamProxy") ?: ""
                    if (key != null) {
                        pendingUpstream = upstreamProxy
                        if (proxyOnly) {
                            startServiceWithKey(key, true)
                        } else {
                            tryPrepareAndStartVpn(key, false)
                        }
                        result.success(true)
                    } else {
                        result.error("INVALID_KEY", "Key is null", null)
                    }
                }
                "stopVpn" -> {
                    stopVpn()
                    result.success(true)
                }
                else -> result.notImplemented()
            }
        }
    }

    private fun tryPrepareAndStartVpn(key: String, proxyOnly: Boolean) {
        val intent = VpnService.prepare(this)
        if (intent != null) {
            pendingKey = key
            pendingProxyOnly = proxyOnly
            startActivityForResult(intent, REQ_VPN_PREPARE)
            methodChannel?.invokeMethod("onLog", "Запрос системных прав на VPN...")
        } else {
            startServiceWithKey(key, proxyOnly)
        }
    }

    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode == REQ_VPN_PREPARE && resultCode == Activity.RESULT_OK) {
            pendingKey?.let {
                startServiceWithKey(it, pendingProxyOnly)
                pendingKey = null
                pendingProxyOnly = false
            }
        } else if (requestCode == REQ_VPN_PREPARE) {
            methodChannel?.invokeMethod("onStateChanged", "error")
            methodChannel?.invokeMethod("onLog", "ОШИБКА: Права на VPN отклонены пользователем")
        }
    }

    private fun startServiceWithKey(key: String, proxyOnly: Boolean) {
        val serviceIntent = Intent(this, BipBopVpnService::class.java).apply {
            action = BipBopVpnService.ACTION_CONNECT
            putExtra("EXTRA_SMART_KEY", key)
            putExtra("EXTRA_PROXY_ONLY", proxyOnly)
            putExtra("EXTRA_UPSTREAM_PROXY", pendingUpstream)
        }
        if (android.os.Build.VERSION.SDK_INT >= android.os.Build.VERSION_CODES.O) {
            startForegroundService(serviceIntent)
        } else {
            startService(serviceIntent)
        }
    }

    private fun stopVpn() {
        val serviceIntent = Intent(this, BipBopVpnService::class.java).apply {
            action = BipBopVpnService.ACTION_STOP
        }
        startService(serviceIntent)
    }
}
