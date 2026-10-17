FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -trimpath -o /loadgen .

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /loadgen /loadgen
ENTRYPOINT ["/loadgen"]
