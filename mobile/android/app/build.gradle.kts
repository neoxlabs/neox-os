plugins {
    id("com.android.application")
    id("kotlin-android")
    // The Flutter Gradle Plugin must be applied after the Android and Kotlin Gradle plugins.
    id("dev.flutter.flutter-gradle-plugin")
}

android {
    namespace = "com.neox.neox"
    compileSdk = flutter.compileSdkVersion
    // 插件里最高的那个. NDK 向后兼容, 所以取最高的那一版 ——
    // 留着不管的话每次构建都刷一屏警告, 而**一屏永远都在的警告
    // 等于没有警告**: 真出问题的那一条会混在里面
    ndkVersion = "27.0.12077973"
    ndkVersion = flutter.ndkVersion

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_11
        targetCompatibility = JavaVersion.VERSION_11
    }

    kotlinOptions {
        jvmTarget = JavaVersion.VERSION_11.toString()
    }

    defaultConfig {
        // TODO: Specify your own unique Application ID (https://developer.android.com/studio/build/application-id.html).
        applicationId = "com.neox.neox"
        // You can update the following values to match your application needs.
        // For more information, see: https://flutter.dev/to/review-gradle-config.
        // 26 = Android 8.0.
        //
        // 不是随手抬的: 这个 App 的保活整个建在 startForegroundService 上,
        // 而它是 26 才有的. 更早的系统上那条路根本不存在 ——
        // 装得上但**它永远不会在早上喊你**, 那比装不上更糟.
        // **只出 arm64**.
        //
        // 这台手机是天玑 9400(arm64-v8a), 而 sherpa-onnx 每个 ABI
        // 带一套 .so —— 四个 ABI 全打进去, APK 从 90MB 涨到 324MB,
        // 其中三份是这台机器永远不会用到的.
        //
        // x86/x86_64 只有模拟器用得上, armeabi-v7a 是 2019 年前的机器。
        // 要发给别人的时候再开 split, 现在没有那个需求
        ndk {
            abiFilters += listOf("arm64-v8a")
        }

        minSdk = 26
        targetSdk = flutter.targetSdkVersion
        versionCode = flutter.versionCode
        versionName = flutter.versionName
    }

    buildTypes {
        release {
            // TODO: Add your own signing config for the release build.
            // Signing with the debug keys for now, so `flutter run --release` works.
            signingConfig = signingConfigs.getByName("debug")
        }
    }
}

flutter {
    source = "../.."
}
