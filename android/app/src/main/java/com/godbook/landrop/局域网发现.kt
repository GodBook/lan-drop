package com.godbook.landrop

import android.content.Context
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.os.Handler
import android.os.Looper
import java.util.ArrayDeque

internal data class DiscoveredLanDropServer(
    val serviceName: String,
    val origin: String
)

internal class LanDropNsdDiscovery(
    context: Context,
    private val callback: Callback
) {
    interface Callback {
        fun onStatusChanged(message: String)
        fun onServersChanged(servers: List<DiscoveredLanDropServer>)
    }

    private val manager = context.applicationContext
        .getSystemService(Context.NSD_SERVICE) as NsdManager
    private val mainHandler = Handler(Looper.getMainLooper())
    private val pendingResolutions = ArrayDeque<NsdServiceInfo>()
    private val activeServiceNames = mutableSetOf<String>()
    private val queuedServiceNames = mutableSetOf<String>()
    private val serversByName = linkedMapOf<String, DiscoveredLanDropServer>()

    private var discoveryListener: NsdManager.DiscoveryListener? = null
    private var generation = 0
    private var resolving = false
    private var resolveToken = 0

    fun start() = onMainThread(::startOnMainThread)

    fun stop() = onMainThread(::stopOnMainThread)

    fun restart() = onMainThread {
        stopOnMainThread()
        val restartGeneration = generation
        mainHandler.postDelayed({
            if (generation == restartGeneration) startOnMainThread()
        }, RESTART_DELAY_MS)
    }

    private fun startOnMainThread() {
        if (discoveryListener != null) return

        generation++
        val currentGeneration = generation
        pendingResolutions.clear()
        activeServiceNames.clear()
        queuedServiceNames.clear()
        serversByName.clear()
        callback.onServersChanged(emptyList())
        callback.onStatusChanged("正在搜索局域网中的电脑…")

        lateinit var listener: NsdManager.DiscoveryListener
        listener = object : NsdManager.DiscoveryListener {
            override fun onDiscoveryStarted(serviceType: String) {
                dispatch(currentGeneration) {
                    callback.onStatusChanged("正在搜索局域网中的电脑…")
                }
            }

            override fun onServiceFound(serviceInfo: NsdServiceInfo) {
                dispatch(currentGeneration) {
                    if (!serviceInfo.serviceType.startsWith(SERVICE_TYPE_PREFIX, ignoreCase = true)) {
                        return@dispatch
                    }
                    val name = serviceInfo.serviceName
                    if (name.isBlank() || !activeServiceNames.add(name)) return@dispatch
                    pendingResolutions.addLast(serviceInfo)
                    queuedServiceNames.add(name)
                    resolveNext()
                }
            }

            override fun onServiceLost(serviceInfo: NsdServiceInfo) {
                dispatch(currentGeneration) {
                    val name = serviceInfo.serviceName
                    activeServiceNames.remove(name)
                    queuedServiceNames.remove(name)
                    if (serversByName.remove(name) != null) emitServers()
                }
            }

            override fun onDiscoveryStopped(serviceType: String) = Unit

            override fun onStartDiscoveryFailed(serviceType: String, errorCode: Int) {
                dispatch(currentGeneration) {
                    discoveryListener = null
                    callback.onStatusChanged("自动发现不可用（错误码 $errorCode），仍可扫码或手动连接")
                }
            }

            override fun onStopDiscoveryFailed(serviceType: String, errorCode: Int) {
                dispatch(currentGeneration) {
                    callback.onStatusChanged("自动发现停止失败（错误码 $errorCode）")
                }
            }
        }

        discoveryListener = listener
        try {
            manager.discoverServices(SERVICE_TYPE, NsdManager.PROTOCOL_DNS_SD, listener)
        } catch (error: RuntimeException) {
            discoveryListener = null
            callback.onStatusChanged("自动发现不可用，仍可扫码或手动连接")
        }
    }

    private fun stopOnMainThread() {
        generation++
        pendingResolutions.clear()
        activeServiceNames.clear()
        queuedServiceNames.clear()
        serversByName.clear()
        val listener = discoveryListener ?: return
        discoveryListener = null
        try {
            manager.stopServiceDiscovery(listener)
        } catch (_: RuntimeException) {
            // Android may already have stopped discovery after a network change.
        }
        // NsdManager has no API to cancel a legacy resolve. Keep `resolving`
        // true until its callback/timeout so a quick restart cannot overlap it.
    }

    @Suppress("DEPRECATION")
    private fun resolveNext() {
        if (resolving || discoveryListener == null) return

        var service = pendingResolutions.pollFirst()
        while (service != null && service.serviceName !in activeServiceNames) {
            queuedServiceNames.remove(service.serviceName)
            service = pendingResolutions.pollFirst()
        }
        service ?: return

        resolving = true
        val token = ++resolveToken
        val requestGeneration = generation
        try {
            manager.resolveService(service, object : NsdManager.ResolveListener {
                override fun onResolveFailed(serviceInfo: NsdServiceInfo, errorCode: Int) {
                    dispatchResolution(token, requestGeneration) {
                        queuedServiceNames.remove(serviceInfo.serviceName)
                    }
                }

                override fun onServiceResolved(serviceInfo: NsdServiceInfo) {
                    dispatchResolution(token, requestGeneration) {
                        val name = serviceInfo.serviceName
                        queuedServiceNames.remove(name)
                        if (name !in activeServiceNames) return@dispatchResolution
                        val hostAddress = serviceInfo.host?.hostAddress ?: return@dispatchResolution
                        val origin = ServerAddress.httpOrigin(hostAddress, serviceInfo.port)
                            ?: return@dispatchResolution
                        serversByName[name] = DiscoveredLanDropServer(name, origin)
                        emitServers()
                    }
                }
            })
        } catch (_: RuntimeException) {
            resolving = false
            queuedServiceNames.remove(service.serviceName)
            resolveNext()
            return
        }

        mainHandler.postDelayed({
            if (resolving && resolveToken == token) {
                resolving = false
                resolveToken++
                resolveNext()
            }
        }, RESOLVE_TIMEOUT_MS)
    }

    private fun dispatchResolution(token: Int, requestGeneration: Int, action: () -> Unit) {
        mainHandler.post {
            if (!resolving || resolveToken != token) return@post
            resolving = false
            if (generation == requestGeneration && discoveryListener != null) action()
            resolveNext()
        }
    }

    private fun emitServers() {
        val servers = serversByName.values.sortedBy { it.serviceName.lowercase() }
        callback.onServersChanged(servers)
        callback.onStatusChanged(
            if (servers.isEmpty()) "正在搜索局域网中的电脑…" else "已发现 ${servers.size} 台电脑"
        )
    }

    private fun dispatch(expectedGeneration: Int, action: () -> Unit) {
        mainHandler.post {
            if (generation == expectedGeneration && discoveryListener != null) action()
        }
    }

    private fun onMainThread(action: () -> Unit) {
        if (Looper.myLooper() == Looper.getMainLooper()) action() else mainHandler.post(action)
    }

    private companion object {
        const val SERVICE_TYPE = "_landrop._tcp."
        const val SERVICE_TYPE_PREFIX = "_landrop._tcp"
        const val RESTART_DELAY_MS = 300L
        const val RESOLVE_TIMEOUT_MS = 10_000L
    }
}
