# orderer.Dockerfile — builds a Fabric orderer image that includes the
# constraint-aware ordering hooks from Paper 3.
#
# Build context: the root of the fabric fork repository.
# Usage (from repo root):
#   docker build -t fabric-orderer-constraint:paper3 \
#       -f evaluation/paper3/network/orderer.Dockerfile .

# ── Stage 1: build ────────────────────────────────────────────────────────────
FROM golang:1.26 AS builder

WORKDIR /build

# Copy the full module so `go build` can resolve all dependencies.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build only the orderer binary — no CGO, static binary for Alpine compat.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -buildvcs=false \
    -ldflags="-s -w" \
    -o /out/orderer \
    ./cmd/orderer

# ── Stage 2: runtime ──────────────────────────────────────────────────────────
# Use the official fabric-orderer image as the base so we keep the same
# entrypoint, CA certificates bundle, and filesystem layout.
# Use 2.5.10 as base but overwrite both the binary and the default config so
# that the 3.x binary (which removed Kafka) does not see the 2.5 Kafka section.
FROM hyperledger/fabric-orderer:2.5.10

# Replace the stock orderer binary with our custom-built one.
COPY --from=builder /out/orderer /usr/local/bin/orderer

# Replace the default orderer.yaml with the 3.x sampleconfig (no Kafka).
# The 2.5 base ships a Kafka section that our 3.x binary rejects with
# "invalid keys: Kafka" on startup.
COPY --from=builder /build/sampleconfig/orderer.yaml /etc/hyperledger/fabric/orderer.yaml

LABEL org.opencontainers.image.description="Fabric orderer with constraint-aware ordering (Paper 3)"
LABEL org.opencontainers.image.source="https://github.com/YOUR_FORK/fabric"
