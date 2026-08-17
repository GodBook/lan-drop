package com.godbook.landrop

import android.annotation.SuppressLint
import android.app.DownloadManager
import android.content.ActivityNotFoundException
import android.content.Intent
import android.graphics.Color
import android.graphics.Typeface
import android.net.Uri
import android.os.Bundle
import android.os.Environment
import android.view.Gravity
import android.view.inputmethod.EditorInfo
import android.webkit.CookieManager
import android.webkit.DownloadListener
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import org.json.JSONArray

/**
 * LAN Drop Android client: a thin native shell around the LAN Drop web UI.
 *
 * Home screen: manual IP:port entry (+ optional PIN) or QR scan, plus a list
 * of recently used servers. Once connected, the full web UI runs inside a
 * WebView with file uploads (chooser) and downloads (DownloadManager) wired.
 */
class MainActivity : AppCompatActivity() {

    private var webView: WebView? = null
    private var fileChooserCallback: ValueCallback<Array<Uri>>? = null
    private lateinit var prefs: android.content.SharedPreferences
    private lateinit var nsdDiscovery: LanDropNsdDiscovery
    private var activityStarted = false
    private var homeVisible = false
    private var nearbyStatus: TextView? = null
    private var nearbyServers: LinearLayout? = null

    private val qrLauncher = registerForActivityResult(ScanContract()) { result ->
        val contents = result.contents ?: return@registerForActivityResult
        if (contents.startsWith("http://", ignoreCase = true) ||
            contents.startsWith("https://", ignoreCase = true)
        ) {
            connectFromUrl(contents)
        } else {
            Toast.makeText(this, "二维码内容不是 LAN Drop 地址", Toast.LENGTH_SHORT).show()
        }
    }

    private val fileChooser = registerForActivityResult(
        ActivityResultContracts.StartActivityForResult()
    ) { result ->
        val callback = fileChooserCallback ?: return@registerForActivityResult
        fileChooserCallback = null
        val uris = mutableListOf<Uri>()
        val data = result.data
        if (result.resultCode == RESULT_OK && data != null) {
            data.data?.let { uris.add(it) }
            data.clipData?.let { clip ->
                for (i in 0 until clip.itemCount) uris.add(clip.getItemAt(i).uri)
            }
        }
        callback.onReceiveValue(uris.toTypedArray())
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        prefs = getSharedPreferences("landrop", MODE_PRIVATE)
        persistRecents(loadRecents())
        nsdDiscovery = LanDropNsdDiscovery(this, object : LanDropNsdDiscovery.Callback {
            override fun onStatusChanged(message: String) {
                if (homeVisible) nearbyStatus?.text = message
            }

            override fun onServersChanged(servers: List<DiscoveredLanDropServer>) {
                if (homeVisible) renderDiscoveredServers(servers)
            }
        })
        CookieManager.getInstance().setAcceptCookie(true)
        showHome()
    }

    override fun onStart() {
        super.onStart()
        activityStarted = true
        if (homeVisible) nsdDiscovery.start()
    }

    override fun onStop() {
        activityStarted = false
        nsdDiscovery.stop()
        super.onStop()
    }

    // ---------- Home screen (programmatic UI) ----------

    @SuppressLint("SetJavaScriptEnabled")
    private fun showHome() {
        webView?.apply {
            stopLoading()
            destroy()
        }
        webView = null
        homeVisible = true
        val dp = { v: Int -> (v * resources.displayMetrics.density).toInt() }

        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER_HORIZONTAL
            setPadding(dp(28), dp(56), dp(28), dp(24))
            setBackgroundColor(Color.parseColor("#0f172a"))
        }

        root.addView(TextView(this).apply {
            text = "⚡ LAN Drop"
            textSize = 32f
            setTextColor(Color.WHITE)
            typeface = Typeface.DEFAULT_BOLD
            gravity = Gravity.CENTER
        })
        root.addView(TextView(this).apply {
            text = "连接同一 Wi-Fi 下的电脑端"
            textSize = 14f
            setTextColor(Color.parseColor("#94a3b8"))
            gravity = Gravity.CENTER
            setPadding(0, dp(6), 0, dp(32))
        })

        val serverInput = EditText(this).apply {
            hint = "IP:端口  例如 192.168.1.5:8087"
            setSingleLine()
            imeOptions = EditorInfo.IME_ACTION_NEXT
            setTextColor(Color.WHITE)
            setHintTextColor(Color.parseColor("#64748b"))
            background.alpha = 60
        }
        root.addView(serverInput)

        val pinInput = EditText(this).apply {
            hint = "PIN 码（扫码连接可留空）"
            setSingleLine()
            inputType = android.text.InputType.TYPE_CLASS_NUMBER
            setTextColor(Color.WHITE)
            setHintTextColor(Color.parseColor("#64748b"))
            background.alpha = 60
            setPadding(0, dp(12), 0, dp(4))
        }
        root.addView(pinInput)

        val btnConnect = Button(this).apply {
            text = "连  接"
            textSize = 16f
            setAllCaps(false)
            setOnClickListener {
                val host = serverInput.text.toString().trim()
                if (host.isEmpty()) {
                    Toast.makeText(context, "请输入服务器地址", Toast.LENGTH_SHORT).show()
                    return@setOnClickListener
                }
                val pin = pinInput.text.toString().trim()
                val base = ServerAddress.normalizeConnectionUrl(host)
                if (base == null) {
                    Toast.makeText(context, "服务器地址格式不正确", Toast.LENGTH_SHORT).show()
                    return@setOnClickListener
                }
                val url = if (pin.isEmpty()) {
                    base
                } else {
                    Uri.parse(base).buildUpon().appendQueryParameter("pin", pin).build().toString()
                }
                connectFromUrl(url)
            }
        }
        root.addView(btnConnect, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ).apply { topMargin = dp(20) })

        val btnScan = Button(this).apply {
            text = "📷 扫码连接"
            textSize = 16f
            setAllCaps(false)
            setOnClickListener {
                qrLauncher.launch(
                    ScanOptions()
                        .setDesiredBarcodeFormats(ScanOptions.QR_CODE)
                        .setPrompt("对准电脑端 LAN Drop 终端里的二维码")
                        .setBeepEnabled(false)
                        .setOrientationLocked(true)
                )
            }
        }
        root.addView(btnScan, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ).apply { topMargin = dp(12) })

        val nearbyHeader = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        nearbyHeader.addView(TextView(this).apply {
            text = "附近电脑"
            textSize = 13f
            setTextColor(Color.parseColor("#94a3b8"))
        }, LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f))
        nearbyHeader.addView(Button(this).apply {
            text = "刷新"
            textSize = 12f
            setAllCaps(false)
            setOnClickListener { nsdDiscovery.restart() }
        })
        root.addView(nearbyHeader, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ).apply { topMargin = dp(24) })

        nearbyStatus = TextView(this).apply {
            text = "正在搜索局域网中的电脑…"
            textSize = 13f
            setTextColor(Color.parseColor("#64748b"))
            setPadding(0, dp(4), 0, dp(4))
        }
        root.addView(nearbyStatus)

        nearbyServers = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
        }
        root.addView(nearbyServers, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ))

        // Recently used servers
        val recents = loadRecents()
        if (recents.isNotEmpty()) {
            root.addView(TextView(this).apply {
                text = "最近连接"
                textSize = 13f
                setTextColor(Color.parseColor("#94a3b8"))
                setPadding(0, dp(28), 0, dp(8))
            })
            for (url in recents) {
                root.addView(Button(this).apply {
                    text = url.removePrefix("http://")
                    setAllCaps(false)
                    textSize = 14f
                    setOnClickListener { connectFromUrl(url) }
                })
            }
        }

        val scroll = android.widget.ScrollView(this).apply { addView(root) }
        setContentView(scroll)
        if (activityStarted) nsdDiscovery.start()
    }

    private fun renderDiscoveredServers(servers: List<DiscoveredLanDropServer>) {
        val container = nearbyServers ?: return
        val dp = { v: Int -> (v * resources.displayMetrics.density).toInt() }
        container.removeAllViews()
        for (server in servers) {
            container.addView(Button(this).apply {
                text = "${server.serviceName}\n${server.origin.removePrefix("http://")}"
                textSize = 14f
                setAllCaps(false)
                setOnClickListener { connectFromUrl(server.origin) }
            }, LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT,
                LinearLayout.LayoutParams.WRAP_CONTENT
            ).apply { topMargin = dp(6) })
        }
    }

    // ---------- Connection ----------

    private fun connectFromUrl(rawUrl: String) {
        val url = ServerAddress.normalizeConnectionUrl(rawUrl)
        if (url == null) {
            Toast.makeText(this, "服务器地址格式不正确", Toast.LENGTH_SHORT).show()
            return
        }
        saveRecent(url)
        openWebView(url)
    }

    @SuppressLint("SetJavaScriptEnabled")
    private fun openWebView(url: String) {
        homeVisible = false
        nsdDiscovery.stop()
        nearbyStatus = null
        nearbyServers = null
        val serverOrigin = requireNotNull(ServerAddress.originOf(url))
        val wv = WebView(this)
        wv.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            allowFileAccess = false
            allowContentAccess = false
            cacheMode = WebSettings.LOAD_DEFAULT
            mixedContentMode = android.webkit.WebSettings.MIXED_CONTENT_NEVER_ALLOW
            userAgentString = userAgentString + " LANDropApp/1.3"
        }
        CookieManager.getInstance().setAcceptThirdPartyCookies(wv, false)

        wv.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                return ServerAddress.originOf(request.url.toString()) != serverOrigin
            }

            override fun onReceivedError(view: WebView, req: WebResourceRequest?, err: android.webkit.WebResourceError?) {
                if (req?.isForMainFrame == true) {
                    Toast.makeText(this@MainActivity, "无法连接服务器，请检查地址与 Wi-Fi", Toast.LENGTH_LONG).show()
                }
            }
        }

        wv.webChromeClient = object : WebChromeClient() {
            override fun onShowFileChooser(
                view: WebView?,
                callback: ValueCallback<Array<Uri>>,
                params: WebChromeClient.FileChooserParams
            ): Boolean {
                fileChooserCallback?.onReceiveValue(null)
                fileChooserCallback = callback
                val intent = Intent(Intent.ACTION_GET_CONTENT).apply {
                    addCategory(Intent.CATEGORY_OPENABLE)
                    type = "*/*"
                    putExtra(Intent.EXTRA_ALLOW_MULTIPLE, true)
                }
                return try {
                    fileChooser.launch(intent)
                    true
                } catch (e: ActivityNotFoundException) {
                    fileChooserCallback = null
                    false
                }
            }
        }

        wv.setDownloadListener(DownloadListener { url, _, contentDisposition, mimeType, _ ->
            try {
                val cookies = CookieManager.getInstance().getCookie(url)
                val req = DownloadManager.Request(Uri.parse(url)).apply {
                    setNotificationVisibility(DownloadManager.Request.VISIBILITY_VISIBLE_NOTIFY_COMPLETED)
                    if (mimeType != null) setMimeType(mimeType)
                    if (cookies != null) addRequestHeader("Cookie", cookies)
                    val name = URLUtilName(contentDisposition, url)
                    setDestinationInExternalPublicDir(Environment.DIRECTORY_DOWNLOADS, name)
                }
                (getSystemService(DOWNLOAD_SERVICE) as DownloadManager).enqueue(req)
                Toast.makeText(this, "已开始下载：系统通知栏查看进度", Toast.LENGTH_SHORT).show()
            } catch (e: Exception) {
                Toast.makeText(this, "下载失败: ${e.message}", Toast.LENGTH_SHORT).show()
            }
        })

        setContentView(wv)
        webView = wv
        wv.loadUrl(url)
    }

    private fun URLUtilName(contentDisposition: String?, url: String): String {
        val cdName = contentDisposition?.let { cd ->
            Regex("filename\\*?=(?:UTF-8'')?\"?([^\";]+)\"?", RegexOption.IGNORE_CASE)
                .find(cd)?.groupValues?.get(1)
        }
        return cdName ?: android.webkit.URLUtil.guessFileName(url, contentDisposition, null)
    }

    // ---------- Recents persistence ----------

    private fun loadRecents(): List<String> {
        val raw = prefs.getString("recents", "[]") ?: "[]"
        val stored = try { JSONArray(raw) } catch (_: Exception) { JSONArray() }
        val candidates = mutableListOf<String>()
        for (i in 0 until stored.length()) {
            val entry = stored.opt(i)
            val rawUrl = when (entry) {
                is String -> entry
                is org.json.JSONObject -> entry.optString("url")
                else -> null
            } ?: continue
            candidates.add(rawUrl)
        }
        return ServerAddress.sanitizeRecentOrigins(candidates, MAX_RECENTS)
    }

    private fun saveRecent(url: String) {
        val origin = ServerAddress.originOf(url) ?: return
        persistRecents((listOf(origin) + loadRecents()).distinct().take(MAX_RECENTS))
    }

    private fun persistRecents(recents: List<String>) {
        val stored = JSONArray()
        recents.forEach(stored::put)
        prefs.edit().putString("recents", stored.toString()).apply()
    }

    // ---------- Back navigation ----------

    override fun onBackPressed() {
        val wv = webView
        if (wv != null && wv.canGoBack()) {
            wv.goBack()
        } else {
            showHome()
        }
    }

    override fun onDestroy() {
        if (::nsdDiscovery.isInitialized) nsdDiscovery.stop()
        fileChooserCallback?.onReceiveValue(null)
        fileChooserCallback = null
        webView?.destroy()
        webView = null
        super.onDestroy()
    }

    private companion object {
        const val MAX_RECENTS = 3
    }
}
