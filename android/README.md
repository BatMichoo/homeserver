# Home Server Android TV app

A thin Android TV (and phone) wrapper around the home server's web UI
(`lan.homeserver.tv`). The server compiles the APK itself, baking in its own
home CA certificate and LAN domain, so the WebView trusts the server's HTTPS
without installing anything on the device. The CA is trusted only for the LAN
domain and its subdomains; cleartext traffic is disallowed.

## How it is built

The `Dockerfile` makes a pinned builder image (Debian, JDK 17, Gradle 8.11.1,
Android SDK platform 35 / build-tools 35.0.0; downloads are SHA-256 verified)
with this project at `/src`. Gradle takes these properties:

| Property | Meaning |
| --- | --- |
| `-PlanDomain` | LAN domain, e.g. `homeserver.lan` |
| `-PhttpsPort`, `-PhttpPort` | server ports (exposed as `BuildConfig.HTTPS_PORT` / `HTTP_PORT`) |
| `-PcaFile` | path to the home CA certificate (PEM) to trust |
| `-PversionCode`, `-PversionName` | APK version |

If `TV_KEYSTORE` and `TV_KEYSTORE_PASS` are set the APK is signed with that
keystore (alias `tv`); otherwise it gets the default debug signature. The
network security config and the CA copy are generated into the Gradle build
directory, never into `src/`.

## Building by hand

```bash
docker build -t hs-android .
docker run --rm -v hs-gradle:/cache/gradle -v /path/to/ca.crt:/ca.crt:ro -v "$PWD/out:/out" hs-android \
  sh -c 'cp -r /src /tmp/b && cd /tmp/b && gradle --no-daemon assembleDebug \
    -PlanDomain=homeserver.lan -PhttpsPort=443 -PhttpPort=80 -PcaFile=/ca.crt \
    -PversionCode=1 -PversionName=1-test && cp app/build/outputs/apk/debug/*.apk /out/'
```

The `hs-gradle` volume keeps downloaded dependencies between builds.
