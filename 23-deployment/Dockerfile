# Build from the REPO ROOT, because go.mod lives there:
#
#   docker build -f 23-deployment/Dockerfile --build-arg VERSION=v1.2.3 -t go-service .
#   docker run --rm -p 8080:8080 go-service
#   curl localhost:8080/version
#
# Multi-stage means two images: a big one with the Go toolchain to compile
# in, and a tiny one containing just the result. Only the last stage ships.

# ---- Stage 1: build ----
# $BUILDPLATFORM runs the compiler natively even when building for another
# CPU. Go cross-compiles, so there's no need to emulate the target.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev

WORKDIR /src

# Docker caches each step until its inputs change. Copying only these two
# first means dependencies are downloaded again only when they change, like
# copying composer.json before your code.
COPY go.mod go.sum ./
RUN go mod download

COPY 23-deployment ./23-deployment

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=$VERSION" \
    -o /out/go-service ./23-deployment

# ---- Stage 2: run ----
# Distroless has no shell or package manager: nothing for an attacker to use.
# It does include CA certificates (for calling HTTPS APIs) and timezone data,
# which an empty `FROM scratch` image doesn't. :nonroot runs as a normal user.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/go-service /go-service

EXPOSE 8080

# There's no curl in here, so the binary checks itself.
HEALTHCHECK CMD ["/go-service", "-healthcheck"]

# Use the JSON (exec) form. `ENTRYPOINT /go-service -serve` would make a shell
# PID 1, and SIGTERM from `docker stop` would never reach Go (lesson 19).
ENTRYPOINT ["/go-service", "-serve"]
