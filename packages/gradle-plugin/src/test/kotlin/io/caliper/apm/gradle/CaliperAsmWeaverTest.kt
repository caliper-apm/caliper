package io.caliper.apm.gradle

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

@Retention(AnnotationRetention.BINARY)
annotation class TestCaliperTrace(val name: String = "", val scene: String = "")

object TestHooks {
    val traceEvents = ArrayList<Triple<String, String, Boolean>>()
    val okHttpBuilders = ArrayList<Any>()

    @JvmStatic
    fun beginTrace(name: String): Long = System.nanoTime()

    @JvmStatic
    fun endTrace(name: String, scene: String, startNanos: Long, failed: Boolean) {
        assertTrue("startNanos must be positive", startNanos > 0L)
        traceEvents.add(Triple(name, scene, failed))
    }

    @JvmStatic
    fun onOkHttpBuilder(builder: Any): Any {
        okHttpBuilders.add(builder)
        return builder
    }
}

class FakeOkHttpClient {
    class Builder {
        fun build(): FakeOkHttpClient = FakeOkHttpClient()
    }
}

class SampleInstrumentedTarget {
    @TestCaliperTrace(name = "checkout_flow", scene = "checkout")
    fun runCheckout(shouldFail: Boolean): String {
        if (shouldFail) throw IllegalStateException("payment_declined")
        return "ok"
    }

    fun createHttpClient(): FakeOkHttpClient = FakeOkHttpClient.Builder().build()
}

class CaliperAsmWeaverTest {
    private class ByteArrayClassLoader(parent: ClassLoader) : ClassLoader(parent) {
        fun define(name: String, bytes: ByteArray): Class<*> = defineClass(name, bytes, 0, bytes.size)
    }

    @Test
    fun weavesAndExecutesCaliperTraceAndOkHttpHooksOnLiveJvm() {
        TestHooks.traceEvents.clear()
        TestHooks.okHttpBuilders.clear()

        val targetClass = SampleInstrumentedTarget::class.java
        val rawBytes = targetClass.classLoader
            .getResourceAsStream(targetClass.name.replace('.', '/') + ".class")!!
            .readBytes()

        val cfg = CaliperAsmConfig(
            instrumentOkHttp = true,
            instrumentCaliperTrace = true,
            includePackages = listOf("io.caliper.apm.gradle"),
            okHttpBuilderOwner = FakeOkHttpClient.Builder::class.java.name.replace('.', '/'),
            okHttpBuildDesc = "()L${FakeOkHttpClient::class.java.name.replace('.', '/')};",
            hooksOwner = TestHooks::class.java.name.replace('.', '/'),
            traceAnnotationDesc = "L${TestCaliperTrace::class.java.name.replace('.', '/')};",
        )
        // Temporarily test with filter allowing sample test package by bypassing hard-exclude on test fixture:
        // Move sample into neutral package via custom weaver config? Let's verify filter separately and weave directly!
        assertFalse(CaliperAsmFilter.shouldInstrument("io.caliper.apm.CaliperClient"))
        assertFalse(CaliperAsmFilter.shouldInstrument("android.app.Activity"))
        assertFalse(CaliperAsmFilter.shouldInstrument("okhttp3.OkHttpClient"))
        assertTrue(CaliperAsmFilter.shouldInstrument("com.example.shop.CheckoutRepo", includePackages = listOf("com.example.")))
        assertFalse(CaliperAsmFilter.shouldInstrument("com.thirdparty.Sdk", includePackages = listOf("com.example.")))

        // Rename internal package in-memory so the sample target sits in `com/example/shop/SampleTarget`
        // (proving realistic non-caliper host app package passes filter and executes woven bytecode cleanly!).
        val cr = org.objectweb.asm.ClassReader(rawBytes)
        val cw = org.objectweb.asm.ClassWriter(cr, org.objectweb.asm.ClassWriter.COMPUTE_FRAMES)
        val remapper = org.objectweb.asm.commons.SimpleRemapper(
            targetClass.name.replace('.', '/'),
            "com/example/shop/SampleTarget",
        )
        val wovenCfg = cfg.copy(includePackages = listOf("com.example.shop"))
        val caliperVisitor = CaliperAsmClassVisitor(org.objectweb.asm.Opcodes.ASM9, cw, wovenCfg)
        cr.accept(
            org.objectweb.asm.commons.ClassRemapper(caliperVisitor, remapper),
            org.objectweb.asm.ClassReader.EXPAND_FRAMES,
        )
        val transformedBytes = cw.toByteArray()

        val loader = ByteArrayClassLoader(targetClass.classLoader)
        val loaded = loader.define("com.example.shop.SampleTarget", transformedBytes)
        val instance = loaded.getDeclaredConstructor().newInstance()

        // 1. Normal @CaliperTrace return -> failed = false
        val res = loaded.getMethod("runCheckout", Boolean::class.javaPrimitiveType).invoke(instance, false)
        assertEquals("ok", res)

        // 2. Throwing @CaliperTrace exit -> failed = true
        var threw = false
        try {
            loaded.getMethod("runCheckout", Boolean::class.javaPrimitiveType).invoke(instance, true)
        } catch (ex: java.lang.reflect.InvocationTargetException) {
            threw = ex.targetException is IllegalStateException
        }
        assertTrue("expected original IllegalStateException to propagate", threw)

        assertEquals(2, TestHooks.traceEvents.size)
        assertEquals(Triple("checkout_flow", "checkout", false), TestHooks.traceEvents[0])
        assertEquals(Triple("checkout_flow", "checkout", true), TestHooks.traceEvents[1])

        // 3. OkHttp Builder.build() weaving -> invokes onOkHttpBuilder automatically!
        val httpClient = loaded.getMethod("createHttpClient").invoke(instance)
        assertTrue(httpClient is FakeOkHttpClient)
        assertEquals(1, TestHooks.okHttpBuilders.size)
        assertTrue(TestHooks.okHttpBuilders[0] is FakeOkHttpClient.Builder)
    }
}
