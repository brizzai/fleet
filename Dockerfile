# Reproducible Linux build + runtime environment for fleet.
#
#   docker build -t fleet .
#   docker run -it --rm -v "$PWD:/repo" -w /repo fleet
#
# The runtime stage is Ubuntu 24.04: its tmux (3.4) supports
# allow-passthrough, which fleet uses when available. gh is not installed
# by default — PR status columns degrade gracefully without it; add it with
#   apt-get install gh
# (or mount your host's gh config) if you want PR integration inside the
# container.

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/fleet ./cmd/fleet

FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends \
        tmux git ca-certificates xclip locales \
    && rm -rf /var/lib/apt/lists/* \
    && locale-gen en_US.UTF-8
ENV LANG=en_US.UTF-8 \
    TERM=xterm-256color
COPY --from=build /out/fleet /usr/local/bin/fleet
ENTRYPOINT ["fleet"]
