package com.godbook.landrop

import java.net.URI
import java.util.Locale

internal object ServerAddress {
    private val supportedSchemes = setOf("http", "https")

    fun normalizeConnectionUrl(rawUrl: String): String? {
        val raw = rawUrl.trim()
        if (raw.isEmpty()) return null

        val hasExplicitScheme = raw.contains("://")
        val candidate = if (
            raw.startsWith("http://", ignoreCase = true) ||
            raw.startsWith("https://", ignoreCase = true)
        ) {
            raw
        } else {
            if (hasExplicitScheme) return null
            "http://$raw"
        }

        val uri = try {
            URI(candidate)
        } catch (_: Exception) {
            return null
        }
        val scheme = uri.scheme?.lowercase(Locale.ROOT) ?: return null
        if (scheme !in supportedSchemes || uri.host.isNullOrBlank() || uri.rawUserInfo != null) {
            return null
        }
        if (uri.port > 65535) return null
        return uri.toASCIIString()
    }

    fun originOf(rawUrl: String): String? {
        val normalized = normalizeConnectionUrl(rawUrl) ?: return null
        val uri = URI(normalized)
        val scheme = uri.scheme.lowercase(Locale.ROOT)
        val port = when {
            scheme == "http" && uri.port == 80 -> -1
            scheme == "https" && uri.port == 443 -> -1
            else -> uri.port
        }
        return try {
            URI(
                scheme,
                null,
                uri.host.lowercase(Locale.ROOT),
                port,
                null,
                null,
                null
            ).toASCIIString()
        } catch (_: Exception) {
            null
        }
    }

    fun httpOrigin(hostAddress: String, port: Int): String? {
        val host = hostAddress.trim().removePrefix("[").removeSuffix("]")
        if (host.isEmpty() || port !in 1..65535) return null
        return try {
            URI("http", null, host, port, null, null, null).toASCIIString()
        } catch (_: Exception) {
            null
        }
    }

    fun sanitizeRecentOrigins(rawUrls: Iterable<String>, limit: Int): List<String> {
        if (limit <= 0) return emptyList()
        val origins = linkedSetOf<String>()
        for (rawUrl in rawUrls) {
            originOf(rawUrl)?.let(origins::add)
            if (origins.size == limit) break
        }
        return origins.toList()
    }
}
