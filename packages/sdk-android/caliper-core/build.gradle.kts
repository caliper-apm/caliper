plugins {
    kotlin("jvm") version "2.1.20"
}

group = "io.caliper.apm"
version = "0.1.0"

kotlin {
    jvmToolchain(21)
}

dependencies {
    testImplementation("junit:junit:4.13.2")
}
