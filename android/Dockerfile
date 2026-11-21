# Builds the Home Server Android TV app. The home server runs this image to
# compile an APK with its own home CA and LAN domain baked in, so the app's
# WebView trusts the server's HTTPS. Everything is pinned and checksummed so a
# rebuild is reproducible and a tampered download fails the build.
FROM debian:bookworm-slim

ARG GRADLE_VERSION=8.11.1
ARG GRADLE_SHA256=f397b287023acdba1e9f6fc5ea72d22dd63669d59ed4a289a29b1a76eee151c6
ARG CMDLINE_TOOLS=commandlinetools-linux-15859902_latest.zip
ARG CMDLINE_TOOLS_SHA256=4e4c464f145a7512b57d088ac6c278c03c9eea610886b35a5e0804e74eedf583

ENV ANDROID_HOME=/opt/android-sdk \
    ANDROID_SDK_ROOT=/opt/android-sdk \
    GRADLE_USER_HOME=/cache/gradle \
    PATH=/opt/gradle/bin:$PATH

RUN apt-get update \
 && apt-get install -y --no-install-recommends openjdk-17-jdk-headless curl unzip ca-certificates \
 && rm -rf /var/lib/apt/lists/*

# Gradle is installed directly (no wrapper in the project) so the build never
# downloads a distribution at run time.
RUN curl -fsSL -o /tmp/gradle.zip "https://services.gradle.org/distributions/gradle-${GRADLE_VERSION}-bin.zip" \
 && echo "${GRADLE_SHA256}  /tmp/gradle.zip" | sha256sum -c - \
 && unzip -q /tmp/gradle.zip -d /opt \
 && mv /opt/gradle-${GRADLE_VERSION} /opt/gradle \
 && rm /tmp/gradle.zip

# The command-line tools are only needed to fetch the platform and build-tools;
# they are deleted afterwards to keep the image small. The accepted licenses
# stay in the SDK so Gradle does not try to prompt for them.
RUN curl -fsSL -o /tmp/cmdline.zip "https://dl.google.com/android/repository/${CMDLINE_TOOLS}" \
 && echo "${CMDLINE_TOOLS_SHA256}  /tmp/cmdline.zip" | sha256sum -c - \
 && mkdir -p /opt/android-sdk/cmdline-tools /tmp/cmdline \
 && unzip -q /tmp/cmdline.zip -d /tmp/cmdline \
 && mv /tmp/cmdline/cmdline-tools /opt/android-sdk/cmdline-tools/latest \
 && (yes | /opt/android-sdk/cmdline-tools/latest/bin/sdkmanager --licenses >/dev/null) \
 && /opt/android-sdk/cmdline-tools/latest/bin/sdkmanager "platforms;android-35" "build-tools;35.0.0" \
 && rm -rf /opt/android-sdk/cmdline-tools /tmp/cmdline /tmp/cmdline.zip

# Non-root; /cache/gradle is meant to be a volume so dependencies download once.
RUN useradd -m -u 1000 builder \
 && mkdir -p /cache/gradle /src \
 && chown -R builder:builder /opt/android-sdk /cache /src

COPY --chown=builder:builder . /src
USER builder
WORKDIR /src

# The loop keeps /out/tv.apk current. It touches /tmp/ready as soon as it
# runs, which is what the healthcheck looks for.
HEALTHCHECK CMD test -f /tmp/ready
ENTRYPOINT ["/src/builder/build-loop.sh"]
