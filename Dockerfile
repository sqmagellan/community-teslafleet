# Multi-stage build → small static image.
FROM golang:1.26.6-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gateway ./cmd/gateway

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gateway /gateway
# Defaults; override with TGW_* env or a mounted /config/config.yaml
ENV TGW_CONFIG=/config/config.yaml
EXPOSE 4460
ENTRYPOINT ["/gateway"]
