# Caliper APM Android SDK — R8 / ProGuard Consumer Keep Rules
# Preserves ASM-woven static hooks, OkHttp reflective bridge, JNI symbols, and WebView JS bridge.

-keep class io.caliper.apm.CaliperAutoInstrumentHooks {
    public static *;
}
-keep @interface io.caliper.apm.CaliperTrace { *; }

-keep class io.caliper.apm.android.CaliperOkHttpNetworkBridge {
    public static *;
}
-keep class io.caliper.apm.android.CaliperOkHttpInterceptor { *; }
-keep class io.caliper.apm.android.CaliperOkHttpEventListener { *; }
-keep class io.caliper.apm.android.CaliperEventListenerFactory { *; }

-keep class io.caliper.apm.android.CaliperNativeBridge {
    native <methods>;
    public static *;
}

-keepclassmembers class io.caliper.apm.android.CaliperWebViewBridge {
    @android.webkit.JavascriptInterface <methods>;
}

# Preserve OkHttp Builder's eventListenerFactory field so existing host EventListeners are wrapped, not replaced.
-keepclassmembers class okhttp3.OkHttpClient$Builder {
    okhttp3.EventListener$Factory eventListenerFactory;
}

# OkHttp is optional (compileOnly); do not fail R8 if host app does not use OkHttp.
-dontwarn okhttp3.**
-dontwarn okio.**
