# Builds the Linux desktop client (AppImage + .deb) in a container, since the
# host does not have webkit2gtk-4.1 installed and we don't want to install
# system packages there. Run via desktop/build/linux.sh, which mounts the
# repo read-write and local-disk cargo caches (never ~/Projects, which is
# NFS).
FROM rust:latest

RUN apt-get update && apt-get install -y --no-install-recommends \
    libwebkit2gtk-4.1-dev \
    libayatana-appindicator3-dev \
    librsvg2-dev \
    libssl-dev \
    libglib2.0-dev \
    libgtk-3-dev \
    libsoup-3.0-dev \
    libjavascriptcoregtk-4.1-dev \
    build-essential \
    curl \
    wget \
    file \
    openssh-client \
    pkg-config \
    && rm -rf /var/lib/apt/lists/*

# cargo-tauri (the `cargo tauri` subcommand) builds AppImage/deb bundles.
RUN cargo install tauri-cli --version "^2" --locked

# desktop/app (Vite + Preact + TypeScript) is bundled into the Tauri
# frontendDist; tauri.conf.json's beforeBuildCommand runs `npm run build`
# there, so the image needs Node.
RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /work
