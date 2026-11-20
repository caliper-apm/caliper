package io.caliper.apm.rn

import io.caliper.apm.CaliperClient

/**
 * React Native NativeModule / TurboModule Kotlin bridge.
 * Attaches RN screens directly onto the Native [CaliperClient] lifecycle, scene context,
 * breadcrumb ring, and offline disk spool — never a disconnected second monitoring pipeline.
 */
class CaliperReactNativeBridge(private val client: CaliperClient) {
    fun setScene(scene: String) {
        client.setScene(scene)
    }

    fun addBreadcrumb(category: String, message: String) {
        client.addBreadcrumb(category, message)
    }

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
