package io.caliper.apm

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class TraceContextTest {
    @Test
    fun generatesAndParsesValidW3CTraceparent() {
        val ctx = CaliperTraceContext.newRoot(sampled = true)
        assertEquals(32, ctx.traceId.length)
        assertEquals(16, ctx.spanId.length)
        assertNotEquals("00000000000000000000000000000000", ctx.traceId)
        assertNotEquals("0000000000000000", ctx.spanId)

        val header = ctx.toTraceparent()
        assertTrue(header.startsWith("00-"))
        assertTrue(header.endsWith("-01"))

        val parsed = CaliperTraceContext.parseTraceparent(header)
        assertNotNull(parsed)
        assertEquals(ctx, parsed)
    }

    @Test
    fun rejectsMalformedOrAllZeroTraceparent() {
        assertNull(CaliperTraceContext.parseTraceparent(null))
        assertNull(CaliperTraceContext.parseTraceparent("invalid"))
        assertNull(CaliperTraceContext.parseTraceparent("ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"))
        assertNull(CaliperTraceContext.parseTraceparent("00-00000000000000000000000000000000-00f067aa0ba902b7-01"))
        assertNull(CaliperTraceContext.parseTraceparent("00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01"))
        assertNull(CaliperTraceContext.parseTraceparent("00-zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz-00f067aa0ba902b7-01"))
    }

    @Test
    fun enforcesPropagationAllowlistAndAvoidsSelfLoop() {
        val collector = "https://apm.mycorp.com/v1/ingest"
        assertFalse(CaliperTraceContext.shouldPropagate("api.mycorp.com", emptyList(), collector))
        assertTrue(CaliperTraceContext.shouldPropagate("api.mycorp.com", listOf("api.mycorp.com"), collector))
        assertTrue(CaliperTraceContext.shouldPropagate("checkout.mycorp.com", listOf(".mycorp.com"), collector))
        assertTrue(CaliperTraceContext.shouldPropagate("mycorp.com", listOf(".mycorp.com"), collector))
        assertFalse(CaliperTraceContext.shouldPropagate("evil-mycorp.com", listOf(".mycorp.com"), collector))
        // Wildcard must NOT inject into collector host itself to avoid self-tracing loops.
        assertFalse(CaliperTraceContext.shouldPropagate("apm.mycorp.com", listOf("*"), collector))
        assertTrue(CaliperTraceContext.shouldPropagate("api.other.com", listOf("*"), collector))
    }
}
