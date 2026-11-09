FROM golang:1.26-bookworm

RUN apt-get update && apt-get install -y --no-install-recommends python3 \
    && rm -rf /var/lib/apt/lists/*

ENV GOMAXPROCS=2 GOCACHE=/tmp/go-cache GOMODCACHE=/tmp/go-mod
WORKDIR /workspace
