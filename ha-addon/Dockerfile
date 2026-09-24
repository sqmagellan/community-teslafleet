# Bundled Home Assistant add-on image — the whole stack in one container:
# fleet-telemetry + the vehicle-command HTTP proxy + the community-teslafleet
# gateway. The gateway acts as the container init and supervises the upstream
# Tesla binaries (TGW_STREAM_EMBEDDED=true). Installing this one add-on yields a
# complete self-hosted setup; the user only provides public reachability + runs
# the onboarding wizard.
#
# Built for linux/amd64 and linux/arm64 — both upstream Tesla images are multi-arch,
# so buildx selects the matching binary per target.

# --- our gateway (static, no cgo: go-zeromq/zmq4 is pure Go) ---
FROM golang:1.26.6-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gateway ./cmd/gateway

# --- upstream Tesla binaries (pulled per target arch by buildx) ---
FROM tesla/fleet-telemetry:latest AS fleet-telemetry
FROM tesla/vehicle-command:latest AS vehicle-command

# --- runtime: debian-slim for broad glibc/CA compatibility with the upstream binaries ---
FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/gateway /usr/local/bin/gateway
COPY --from=fleet-telemetry /fleet-telemetry /usr/local/bin/fleet-telemetry
COPY --from=vehicle-command /usr/local/bin/tesla-http-proxy /usr/local/bin/tesla-http-proxy

# All-in-one: the gateway supervises fleet-telemetry (+ proxy when commands are on)
# and serves the onboarding wizard (reached via HA ingress on :8099).
ENV TGW_STREAM_EMBEDDED=true \
    TGW_ONBOARD_ENABLED=true
# 4443 = fleet-telemetry mTLS (the car dials in); 4460 = Fleet API + WSS for TeslaMate.
EXPOSE 4443 4460
ENTRYPOINT ["/usr/local/bin/gateway"]
