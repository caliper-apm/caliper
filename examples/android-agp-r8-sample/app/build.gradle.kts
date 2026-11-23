plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("io.caliper.apm.autoinstrument")
}

android {
    namespace = "com.caliper.sample"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.caliper.sample"
        minSdk = 26
        targetSdk = 35
        versionCode = 190
        versionName = "1.9.0"

        ndk {
            abiFilters += listOf("arm64-v8a")
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = true
            isShrinkResources = false
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro",
            )
            // Use debug signing only so local `./gradlew assembleRelease` produces a verifiable APK.
            signingConfig = signingConfigs.getByName("debug")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
}

caliperAutoInstrument {
    enabled.set(true)
    instrumentOkHttp.set(true)
    instrumentCaliperTrace.set(true)
    includePackages.set(listOf("com.caliper.sample"))
}

dependencies {
    implementation(project(":caliper-android"))
    implementation("com.squareup.okhttp3:okhttp:4.12.0")
    testImplementation("junit:junit:4.13.2")
}
