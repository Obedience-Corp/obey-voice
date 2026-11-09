FROM golang:1.26-bookworm

RUN apt-get purge -y python3 python3-minimal python3.11 python3.11-minimal \
    && ! command -v python3 && ! command -v python

ENV GOMAXPROCS=2 GOCACHE=/tmp/go-cache GOMODCACHE=/tmp/go-mod
WORKDIR /workspace
