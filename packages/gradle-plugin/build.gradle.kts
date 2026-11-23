plugins {
    kotlin("jvm") version "2.1.20"
    `java-gradle-plugin`
}

group = "io.caliper.apm"
version = "1.9.0"

kotlin {
    jvmToolchain(21)
}

gradlePlugin {
    plugins {
        create("caliperSymbols") {
            id = "io.caliper.apm.symbols"
            displayName = "Caliper APM Symbols & Release Registration Plugin"
            description = "Automatically registers Android releases and uploads R8/Hermes symbol maps to Caliper APM."
            implementationClass = "io.caliper.apm.gradle.CaliperSymbolsPlugin"
        }
        create("caliperAutoInstrument") {
            id = "io.caliper.apm.autoinstrument"
            displayName = "Caliper APM Zero-Boilerplate ASM Auto-Instrumentation Plugin"
            description = "Compile-time allowlisted ASM bytecode weaving for OkHttp, @CaliperTrace, and Activity lifecycle TTID."
            implementationClass = "io.caliper.apm.gradle.CaliperAutoInstrumentPlugin"
        }
    }
}

dependencies {
    implementation("org.ow2.asm:asm:9.7.1")
    implementation("org.ow2.asm:asm-commons:9.7.1")
    compileOnly("com.android.tools.build:gradle-api:8.7.3")
    testImplementation("junit:junit:4.13.2")
    testImplementation("com.squareup.okhttp3:okhttp:4.12.0")
}
