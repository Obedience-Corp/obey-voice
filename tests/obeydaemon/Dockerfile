FROM golang:1.26-bookworm
ARG FIXTURE_UID=1000
ARG FIXTURE_GID=1000
RUN apt-get update && apt-get install -y --no-install-recommends python3 \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --non-unique --gid ${FIXTURE_GID} fixture \
 && useradd --non-unique --uid ${FIXTURE_UID} --gid ${FIXTURE_GID} --create-home --home-dir /fixture fixture
ENV HOME=/fixture GOCACHE=/fixture/go-cache GOMODCACHE=/fixture/go-modules \
 PATH=/fixture/bin:/fixture/.obey/installer/bin:/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
 USER=fixture LOGNAME=fixture GOTOOLCHAIN=local GOWORK=off
USER fixture
WORKDIR /voice
