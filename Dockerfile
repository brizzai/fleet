# Reproducible Linux build + runtime environment for fleet.
#
#   docker build -t fleet .
#   docker run -it --rm -v "$PWD:/repo" -w /repo fleet
#
# The runtime stage is Ubuntu 24.04: its tmux (3.4) supports
# allow-passthrough, which fleet uses when available.
#
# This image deliberately does NOT install an agent CLI (claude/codex/
# opencode): fleet supports three, none are apt packages, and their install
# methods change independently of fleet's own releases. Pressing `a`/`A` to
# create a session will fail with "executable not found" until you install
# at least one, e.g. by extending this image:
#   RUN curl -fsSL https://claude.ai/install.sh | bash
# gh is left out for the same reason — PR status columns degrade
# gracefully without it; add it (`apt-get install gh`) or mount your host's
# gh config if you want PR integration inside the container.
#
# No display server is available in a plain container, so there's nothing
# for a clipboard tool to reach — copy-mode always uses the OSC 52 fallback
# here regardless of what's installed.

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
        tmux git ca-certificates locales \
    && rm -rf /var/lib/apt/lists/* \
    && locale-gen en_US.UTF-8
ENV LANG=en_US.UTF-8 \
    TERM=xterm-256color
# The container runs as root over a bind-mounted host directory, whose files
# are owned by the host UID — git >= 2.35.2 (Ubuntu 24.04 ships 2.43) refuses
# to operate on a repo it doesn't own ("detected dubious ownership") unless
# told to trust it. Without this, fleet's branch/dirty detection and
# worktree creation would silently fail inside the container.
RUN git config --system --add safe.directory '*'
COPY --from=build /out/fleet /usr/local/bin/fleet
ENTRYPOINT ["fleet"]
