# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH

# server for the image's platform
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" -o /out/typehaus-server ./cmd/typehaus-server

# agents for every desktop platform, served under /downloads
RUN set -e; mkdir -p /out/agents; \
    for t in windows/amd64 windows/arm64 darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do \
      os=${t%/*}; arch=${t#*/}; ext=""; [ "$os" = windows ] && ext=".exe"; \
      CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/agents/typehaus-agent-$os-$arch$ext ./cmd/typehaus-agent; \
      if [ "$os" = windows ]; then \
        CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -H=windowsgui -X main.version=${VERSION}" \
          -o /out/agents/typehaus-agentw-$os-$arch.exe ./cmd/typehaus-agent; \
      fi; \
    done; \
    mkdir -p /out/data

# CI places agents that need native toolchains (macOS menu-bar app, built
# on a macOS runner) in dist/agents; they replace the cross-compiled ones.
# "dis[t]" makes the copy optional for plain `docker build .`.
COPY go.mod dis[t] /prebuilt/
RUN if [ -d /prebuilt/agents ]; then cp -v /prebuilt/agents/typehaus-agent* /out/agents/; fi

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/typehaus-server /usr/local/bin/typehaus-server
COPY --from=build /out/agents /app/agents
# pre-create the volume mount point owned by the nonroot user (uid 65532)
COPY --from=build --chown=65532:65532 /out/data /data
ENV TYPEHAUS_DATA=/data \
    TYPEHAUS_LISTEN=:8080 \
    TYPEHAUS_AGENTS_DIR=/app/agents
VOLUME /data
EXPOSE 8080
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["/usr/local/bin/typehaus-server", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/typehaus-server"]
