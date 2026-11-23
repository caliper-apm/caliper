package io.caliper.apm

import java.net.URI
import java.security.SecureRandom

/**
 * W3C Trace Context Level 1 (`traceparent`) generator, parser, and host-allowlist evaluator.
 *
 * Format: `00-<32-lower-hex-trace-id>-<16-lower-hex-span-id>-<2-hex-flags>`
 * Example: `00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01`
 */
data class CaliperTraceContext(
    val traceId: String,
    val spanId: String,
    val sampled: Boolean = true,
) {
    fun toTraceparent(): String =
        "00-$traceId-$spanId-${if (sampled) "01" else "00"}"

    companion object {
        const val HEADER_NAME: String = "traceparent"
        private const val ZERO_TRACE_ID = "00000000000000000000000000000000"
        private const val ZERO_SPAN_ID = "0000000000000000"
        private val HEX = "0123456789abcdef".toCharArray()
        private val rng = SecureRandom()

        fun newRoot(sampled: Boolean = true): CaliperTraceContext {
            var traceId: String
            do {
                traceId = randomHex(16)
            } while (traceId == ZERO_TRACE_ID)

            var spanId: String
            do {
                spanId = randomHex(8)
            } while (spanId == ZERO_SPAN_ID)

            return CaliperTraceContext(traceId = traceId, spanId = spanId, sampled = sampled)
        }

        fun parseTraceparent(raw: String?): CaliperTraceContext? {
            val trimmed = raw?.trim()?.lowercase() ?: return null
            val parts = trimmed.split('-')
            if (parts.size < 4) return null
            val version = parts[0]
            val traceId = parts[1]
            val spanId = parts[2]
            val flags = parts[3]
            if (version.length != 2 || !isLowerHex(version) || version == "ff") return null
            if (traceId.length != 32 || !isLowerHex(traceId) || traceId == ZERO_TRACE_ID) return null
            if (spanId.length != 16 || !isLowerHex(spanId) || spanId == ZERO_SPAN_ID) return null
            if (flags.length != 2 || !isLowerHex(flags)) return null
            val flagByte = flags.toInt(16)
            return CaliperTraceContext(
                traceId = traceId,
                spanId = spanId,
                sampled = (flagByte and 0x01) != 0,
            )
        }

        /**
         * Determines whether `traceparent` should be injected for [host], respecting explicit
         * allowlists (`*`, exact hostname, or `.domain.com` suffix) and blocking self-tracing
         * loops against [collectorEndpointUrl].
         */
        fun shouldPropagate(
            host: String?,
            targets: List<String>,
            collectorEndpointUrl: String? = null,
        ): Boolean {
            if (targets.isEmpty()) return false
            val normHost = host?.trim()?.lowercase()?.trimEnd('.') ?: return false
            if (normHost.isEmpty()) return false

            val collectorHost = extractHost(collectorEndpointUrl)
            if (collectorHost != null && collectorHost == normHost && !targets.any { it.trim().equals(normHost, ignoreCase = true) }) {
                return false
            }

            for (rawRule in targets) {
                val rule = rawRule.trim().lowercase()
                if (rule.isEmpty()) continue
                if (rule == "*") return true
                if (rule.startsWith(".")) {
                    val bare = rule.removePrefix(".")
                    if (normHost == bare || normHost.endsWith(rule)) return true
                } else if (normHost == rule) {
                    return true
                }
            }
            return false
        }

        fun extractHost(url: String?): String? {
            if (url.isNullOrBlank()) return null
            return runCatching { URI(url).host?.lowercase()?.trimEnd('.') }.getOrNull()
        }

        private fun randomHex(byteCount: Int): String {
            val bytes = ByteArray(byteCount)
            rng.nextBytes(bytes)
            val chars = CharArray(byteCount * 2)
            for (i in 0 until byteCount) {
                val v = bytes[i].toInt() and 0xFF
                chars[i * 2] = HEX[v ushr 4]
                chars[i * 2 + 1] = HEX[v and 0x0F]
            }
            return String(chars)
        }

        private fun isLowerHex(s: String): Boolean {
            for (ch in s) {
                val ok = (ch in '0'..'9') || (ch in 'a'..'f')
                if (!ok) return false
            }
            return true
        }
    }
}
