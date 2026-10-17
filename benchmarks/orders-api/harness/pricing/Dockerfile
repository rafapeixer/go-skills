FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -trimpath -o /pricing .

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /pricing /pricing
ENTRYPOINT ["/pricing"]
