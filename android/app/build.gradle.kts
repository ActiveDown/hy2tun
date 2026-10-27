import java.util.Properties
import org.jetbrains.kotlin.gradle.dsl.JvmTarget

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

// -PversionName=0.7.3 (scripts/build_android.sh passes the release version)
val appVersion = (findProperty("versionName") as String?) ?: "0.7.3"
val appVersionCode = appVersion.substringBefore('-').split('.').map { it.toIntOrNull() ?: 0 }
    .let { (it + listOf(0, 0, 0)).take(3) }.let { it[0] * 10000 + it[1] * 100 + it[2] }

// Release signing from android/keystore.properties (storeFile, storePassword,
// keyAlias, keyPassword); without it only the debug APK can be built.
val keystoreProps = Properties().apply {
    val f = rootProject.file("keystore.properties")
    if (f.exists()) f.inputStream().use { load(it) }
}

android {
    namespace = "com.activedown.hy2tun"
    compileSdk = 36
    // also used by scripts/build_android.sh for libhy2tun.so; Gradle installs it when missing
    ndkVersion = "27.0.12077973"

    defaultConfig {
        applicationId = "com.activedown.hy2tun"
        minSdk = 26
        targetSdk = 36
        versionCode = appVersionCode
        versionName = appVersion
    }

    signingConfigs {
        if (keystoreProps.containsKey("storeFile")) {
            create("release") {
                storeFile = rootProject.file(keystoreProps.getProperty("storeFile"))
                storePassword = keystoreProps.getProperty("storePassword")
                keyAlias = keystoreProps.getProperty("keyAlias")
                keyPassword = keystoreProps.getProperty("keyPassword")
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
            signingConfig = signingConfigs.findByName("release")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    packaging {
        // libhy2tun.so is loaded from the APK without extracting
        jniLibs.useLegacyPackaging = false
    }
}

kotlin {
    compilerOptions { jvmTarget.set(JvmTarget.JVM_17) }
}

dependencies {
    implementation("androidx.core:core-ktx:1.16.0")
    implementation("androidx.activity:activity-ktx:1.10.1")
    implementation("com.journeyapps:zxing-android-embedded:4.3.0")
}
