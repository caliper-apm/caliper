// Production Android Library module (com.android.library) — Zero React Native dependencies.
// Build inside Android Studio / AGP 8.7+ with compileSdk 35, minSdk 26 (graceful fallback on 24-25).
plugins {
    id("com.android.library") version "8.7.3"
    id("org.jetbrains.kotlin.android") version "2.1.20"
}

android {
    namespace = "io.caliper.apm.android"
    compileSdk = 35

    defaultConfig {
        minSdk = 26
        consumerProguardFiles("consumer-rules.pro")
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
}

dependencies {
    api(project(":caliper-core"))
    compileOnly("com.squareup.okhttp3:okhttp:4.12.0")
}
