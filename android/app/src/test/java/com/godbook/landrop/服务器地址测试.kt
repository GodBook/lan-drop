package com.godbook.landrop

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class ServerAddressTest {
    @Test
    fun `recent origin drops PIN path query and fragment`() {
        assertEquals(
            "http://192.168.1.5:8087",
            ServerAddress.originOf("http://192.168.1.5:8087/share?pin=1234&x=1#secret")
        )
    }

    @Test
    fun `origin accepts an address without a scheme`() {
        assertEquals(
            "http://landrop.local:8087",
            ServerAddress.originOf("landrop.local:8087/?pin=9876")
        )
    }

    @Test
    fun `origin preserves HTTPS and explicit ports`() {
        assertEquals(
            "https://example.local:8443",
            ServerAddress.originOf("HTTPS://Example.Local:8443/a?q=1")
        )
    }

    @Test
    fun `NSD IPv6 addresses produce valid origins`() {
        assertEquals("http://[fe80::1]:8087", ServerAddress.httpOrigin("fe80::1", 8087))
        assertEquals(
            "http://[fe80::1%wlan0]:8087",
            ServerAddress.originOf(ServerAddress.httpOrigin("fe80::1%wlan0", 8087)!!)
        )
    }

    @Test
    fun `equivalent default ports have the same origin`() {
        assertEquals("http://example.local", ServerAddress.originOf("http://example.local:80/a"))
        assertEquals("https://example.local", ServerAddress.originOf("https://example.local:443/a"))
    }

    @Test
    fun `legacy recents are sanitized deduplicated and capped in order`() {
        assertEquals(
            listOf("http://one.local:8087", "https://two.local", "http://three.local:9000"),
            ServerAddress.sanitizeRecentOrigins(
                listOf(
                    "http://one.local:8087/?pin=1234",
                    "http://one.local:8087/path?pin=5678#secret",
                    "ftp://ignored.local/file",
                    "https://two.local:443/?pin=9999",
                    "http://three.local:9000/?pin=0000",
                    "http://four.local:8087/?pin=1111"
                ),
                limit = 3
            )
        )
    }

    @Test
    fun `non HTTP schemes and malformed hosts are rejected`() {
        assertNull(ServerAddress.originOf("ftp://192.168.1.5/file"))
        assertNull(ServerAddress.originOf("http:///missing-host?pin=1234"))
        assertNull(ServerAddress.originOf("http://user:pass@192.168.1.5:8087"))
    }
}
