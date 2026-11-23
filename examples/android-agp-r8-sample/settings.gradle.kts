pluginManagement {
    includeBuild("../../packages/gradle-plugin")
    plugins {
        id("com.android.application") version "8.7.3"
        id("com.android.library") version "8.7.3"
        id("org.jetbrains.kotlin.android") version "2.1.20"
        kotlin("jvm") version "2.1.20"
    }
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}

dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google()
        mavenCentral()
    }
}

rootProject.name = "caliper-agp-r8-sample"
include(":app", ":caliper-core", ":caliper-android")
project(":caliper-core").projectDir = file("../../packages/sdk-android/caliper-core")
project(":caliper-android").projectDir = file("../../packages/sdk-android/caliper-android")
