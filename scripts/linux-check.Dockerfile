FROM rust:1.95-bookworm
RUN apt-get update && apt-get install -y --no-install-recommends \
    libdbus-1-dev libxkbcommon-dev dbus-x11 python3-dbus python3-gi \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /workspace
ENV CARGO_TARGET_DIR=/target
ENV CARGO_BUILD_JOBS=2
CMD ["dbus-run-session", "--", "cargo", "test", "--locked", "--", "--include-ignored"]
