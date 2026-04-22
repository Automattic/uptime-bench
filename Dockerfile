FROM golang:1.26 AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /out/uptime-bench-harness ./cmd/harness && \
    CGO_ENABLED=0 go build -o /out/uptime-bench-target  ./cmd/target  && \
    CGO_ENABLED=0 go build -o /out/uptime-bench-dns     ./cmd/dns


FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/uptime-bench-harness /usr/local/bin/
COPY --from=builder /out/uptime-bench-target  /usr/local/bin/
COPY --from=builder /out/uptime-bench-dns     /usr/local/bin/
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh

RUN chmod +x /usr/local/bin/entrypoint.sh && \
    mkdir -p /etc/uptime-bench

ENTRYPOINT ["entrypoint.sh"]
