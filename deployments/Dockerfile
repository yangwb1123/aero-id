# syntax=docker/dockerfile:1

FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/aero-id ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/migrate ./cmd/migrate

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source="https://github.com/aero/aero-id"
LABEL org.opencontainers.image.description="aero-id — distributed account aggregation and control plane"
LABEL org.opencontainers.image.version="0.1.0"
WORKDIR /app
COPY --from=build /out/aero-id /app/aero-id
COPY --from=build /out/migrate /app/migrate
COPY configs/config.yaml /app/configs/config.yaml
COPY migrations/ /app/migrations
EXPOSE 8080 9090
USER nonroot:nonroot
ENTRYPOINT ["/app/aero-id", "-config", "/app/configs/config.yaml"]
