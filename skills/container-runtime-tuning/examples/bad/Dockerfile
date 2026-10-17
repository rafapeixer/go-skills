FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
# MISTAKE: GOARCH hardcoded to amd64 and a single-platform build. On arm64 nodes
# the pod fails with "exec format error", and the cheaper arm64 capacity is unusable.
RUN GOOS=linux GOARCH=amd64 go build -o /out/server .

# MISTAKE: alpine has no tzdata package, and the binary does not import time/tzdata,
# so time.LoadLocation("America/Sao_Paulo") fails at startup in production only.
# It also ships a shell and a package manager the service never uses.
FROM alpine:3.22
COPY --from=build /out/server /server
# MISTAKE: no USER; the process runs as root.
ENTRYPOINT ["/server"]
