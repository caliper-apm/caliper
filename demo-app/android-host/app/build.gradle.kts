plugins {
    id("com.android.application") version "8.7.3"
    id("org.jetbrains.kotlin.android") version "2.1.20"
}

android {
    namespace = "com.caliper.demo"
    compileSdk = 35
    defaultConfig {
        applicationId = "dev.caliper.demo"
        minSdk = 26
        targetSdk = 35
        versionCode = 110
        versionName = "1.1.0"
    }
    buildTypes {
        release {
            isMinifyEnabled = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
}

dependencies {
    implementation("io.caliper.apm:caliper-core:1.1.0")
    implementation("com.squareup.okhttp3:okhttp:4.12.0")
}
