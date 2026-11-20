plugins {
    kotlin("jvm") version "2.1.20"
    `java-gradle-plugin`
}

group = "io.caliper.apm"
version = "0.5.0"

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
    }
}

dependencies {
    testImplementation("junit:junit:4.13.2")
}
