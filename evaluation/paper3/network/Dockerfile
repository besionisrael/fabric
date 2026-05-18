FROM golang:1.22-alpine AS builder
WORKDIR /build
COPY . .
RUN go build -o zkcoordinator .

FROM alpine:3.19
COPY --from=builder /build/zkcoordinator /usr/local/bin/zkcoordinator
EXPOSE 8080
ENTRYPOINT ["zkcoordinator"]
