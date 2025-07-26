# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Quellcode kopieren
COPY . .

# Kompilieren, statisch (ohne CGO)
RUN CGO_ENABLED=0 GOOS=linux go build -o sidecar .

# Runtime stage
FROM scratch

# CA Zertifikate (für HTTPS)
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Binärdatei kopieren
COPY --from=builder /app/sidecar /sidecar

# Benutzer (optional, Sicherheit)
USER 1000:1000

ENTRYPOINT ["/sidecar"]
