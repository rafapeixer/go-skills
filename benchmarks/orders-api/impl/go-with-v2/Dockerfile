# Same build for every Go implementation, so the build is not a variable.
# go mod tidy resolves whatever dependencies the generated code imports.
FROM golang:1.26 AS build
WORKDIR /src
COPY . .
RUN go mod tidy && CGO_ENABLED=0 go build -trimpath -o /app .

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /app /app
EXPOSE 8080
ENTRYPOINT ["/app"]
