package io.caliper.apm.rn

import io.caliper.apm.CaliperClient

/**
 * React Native NativeModule / TurboModule Kotlin bridge matching [CaliperNativeBridgeSpec].
 * Attaches RN screens directly onto the Native [CaliperClient] lifecycle, scene context,
 * breadcrumb ring, and offline disk spool — never a disconnected second monitoring pipeline.
 *
 * Host apps expose this bridge to React Native via a thin `ReactContextBaseJavaModule`
 * (or TurboModule) forwarding `@ReactMethod` calls to these exact method names.
 */
class CaliperReactNativeBridge(
    private val client: CaliperClient,
    private val onFirstFrameReady: ((Double) -> Unit)? = null,
) {
    companion object {
        const val MODULE_NAME = "CaliperReactNativeBridge"
    }

    fun setScene(scene: String) {
        client.setScene(scene)
    }

    fun addBreadcrumb(category: String, message: String) {
        client.addBreadcrumb(category, message)
    }

    fun notifyFirstFrameReady(jsEntryToFirstFrameMs: Double) {
        if (!jsEntryToFirstFrameMs.isFinite() || jsEntryToFirstFrameMs < 0.0) return
        if (onFirstFrameReady != null) {
            onFirstFrameReady.invoke(jsEntryToFirstFrameMs)
        } else {
            client.recordCustomJourney(
                journeyName = "rn_first_frame",
                status = "success",
                durationMs = jsEntryToFirstFrameMs,
            )
        }
    }

    /** Primary bridge method matching TypeScript `CaliperNativeBridgeSpec.enqueueEvent`. */
    fun enqueueEvent(kind: String, scene: String, windowId: String?, payloadJson: String) {
        val bypass = kind == "crash" || kind == "anr" || kind == "abnormal_exit"
        client.enqueue(
            kind = kind,
            payloadJson = payloadJson,
            scene = scene.ifEmpty { null },
            windowId = windowId,
            bypassSampling = bypass,
        )
        if (bypass) {
            client.flushAsync()
        }
    }

    /** Legacy alias retained for backward compatibility. */
    fun emitEnvelopeEvent(kind: String, payloadJson: String, scene: String?, windowId: String?, bypassSampling: Boolean) {
        client.enqueue(
            kind = kind,
            payloadJson = payloadJson,
            scene = scene,
            windowId = windowId,
            bypassSampling = bypassSampling,
        )
    }

    fun flush() {
        client.flushAsync()
    }
}
