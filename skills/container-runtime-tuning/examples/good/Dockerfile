# syntax=docker/dockerfile:1
# Build for both architectures in one image index:
#   docker buildx build --platform linux/amd64,linux/arm64 -t registry.example.com/api:1.0.0 --push .

# The builder runs natively on the build machine and cross-compiles, so no emulation is needed.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
# CGO_ENABLED=0 gives a static binary that needs no libc in the runtime image.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/server .

# distroless/static ships CA certificates, tzdata, /etc/passwd and /tmp, and no shell.
# time/tzdata in the binary keeps time zones working if this becomes FROM scratch.
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/server /server
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/server"]
