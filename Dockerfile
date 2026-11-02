FROM golang:1.27.3-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/orbit ./cmd/orbit

FROM debian:trixie-slim AS console-assets
RUN apt-get update && apt-get install -y --no-install-recommends novnc && mkdir -p /out/novnc && cp -aL /usr/share/novnc/core /usr/share/novnc/vendor /out/novnc/ && cp /usr/share/doc/novnc/copyright /out/novnc/COPYRIGHT.txt

FROM debian:trixie-slim
ENV GOMEMLIMIT=384MiB GOMAXPROCS=2 ORBIT_DATA_DIR=/data PORT=8080 ORBIT_STATIC_DIR=/app/dist ORBIT_NOVNC_ROOT=/opt/orbit-novnc
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates xorriso && rm -rf /var/lib/apt/lists/* && useradd --uid 10001 --create-home orbit && mkdir /data && chown orbit:orbit /data
COPY --from=build /out/orbit /usr/local/bin/orbit
COPY --from=console-assets /out/novnc /opt/orbit-novnc
COPY dist ./dist
USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=15s --retries=3 CMD ["orbit","healthcheck"]
ENTRYPOINT ["orbit"]
