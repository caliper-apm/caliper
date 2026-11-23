pluginManagement {
    plugins {
        kotlin("jvm") version "2.1.20"
        id("org.jetbrains.kotlin.android") version "2.1.20"
        id("com.android.library") version "8.7.3"
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

rootProject.name = "caliper-sdk-android"
include(":caliper-core")
