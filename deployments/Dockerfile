# syntax=docker/dockerfile:1

FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/aero-id ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/aero-id /app/aero-id
COPY configs/config.yaml /app/configs/config.yaml
EXPOSE 8080 9090
USER nonroot:nonroot
ENTRYPOINT ["/app/aero-id", "-config", "/app/configs/config.yaml"]
