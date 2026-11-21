import java.io.File

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

// Per-server values, passed as -P properties by the build loop.
val lanDomain = (findProperty("lanDomain") as String?) ?: "homeserver.lan"
val httpsPort = ((findProperty("httpsPort") as String?) ?: "443").toInt()
val httpPort = ((findProperty("httpPort") as String?) ?: "80").toInt()
val caFile = findProperty("caFile") as String?
val appVersionCode = ((findProperty("versionCode") as String?) ?: "1").toInt()
val appVersionName = (findProperty("versionName") as String?) ?: "1"

// The domain is written into XML and a Kotlin string, so reject anything that
// is not a plain hostname.
require(Regex("[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?").matches(lanDomain)) {
    "lanDomain must be a plain hostname, got '$lanDomain'"
}

android {
    namespace = "lan.homeserver.tv"
    compileSdk = 35
    buildToolsVersion = "35.0.0"

    defaultConfig {
        applicationId = "lan.homeserver.tv"
        minSdk = 24
        targetSdk = 35
        versionCode = appVersionCode
        versionName = appVersionName

        buildConfigField("String", "LAN_DOMAIN", "\"$lanDomain\"")
        buildConfigField("int", "HTTPS_PORT", "$httpsPort")
        buildConfigField("int", "HTTP_PORT", "$httpPort")
    }

    buildFeatures {
        buildConfig = true
    }

    // Signed with the server's keystore when provided, so updates install over
    // the previous build; otherwise the default debug key is used.
    val keystore = System.getenv("TV_KEYSTORE")
    if (!keystore.isNullOrEmpty()) {
        signingConfigs {
            create("tv") {
                storeFile = File(keystore)
                storePassword = System.getenv("TV_KEYSTORE_PASS")
                keyAlias = "tv"
                keyPassword = System.getenv("TV_KEYSTORE_PASS")
            }
        }
        buildTypes.configureEach { signingConfig = signingConfigs.getByName("tv") }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlin {
        compilerOptions {
            jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17)
        }
    }
}

// Generates the network security config and the raw CA copy into the build
// directory (never src/), so the home CA is trusted for the LAN domain and its
// subdomains only; everything else keeps using the system trust store.
abstract class GenerateTrustConfig : DefaultTask() {
    @get:Input abstract val domain: Property<String>
    @get:InputFile abstract val ca: RegularFileProperty
    @get:OutputDirectory abstract val outputDir: DirectoryProperty

    @TaskAction
    fun generate() {
        val out = outputDir.get().asFile
        out.deleteRecursively()
        File(out, "raw").mkdirs()
        File(out, "xml").mkdirs()
        ca.get().asFile.copyTo(File(out, "raw/home_ca"))
        File(out, "xml/network_security_config.xml").writeText(
            """<?xml version="1.0" encoding="utf-8"?>
<network-security-config>
    <base-config cleartextTrafficPermitted="false" />
    <domain-config cleartextTrafficPermitted="false">
        <domain includeSubdomains="true">${domain.get()}</domain>
        <trust-anchors>
            <certificates src="@raw/home_ca" />
            <certificates src="system" />
        </trust-anchors>
    </domain-config>
</network-security-config>
"""
        )
    }
}

androidComponents {
    onVariants { variant ->
        val task = tasks.register<GenerateTrustConfig>("generate${variant.name.replaceFirstChar { it.uppercase() }}TrustConfig") {
            domain.set(lanDomain)
            ca.set(
                layout.file(provider {
                    File(caFile ?: throw GradleException("-PcaFile=<path to the home CA certificate> is required"))
                })
            )
        }
        variant.sources.res?.addGeneratedSourceDirectory(task, GenerateTrustConfig::outputDir)
    }
}
