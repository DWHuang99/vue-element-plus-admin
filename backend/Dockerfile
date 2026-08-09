# syntax=docker/dockerfile:1

# ---- Build stage ----
FROM golang:1.26-alpine AS build
WORKDIR /app

# Cache module downloads separately from source copy.
COPY go.mod go.sum ./
RUN go mod download

# Build the server binary (cmd/server: auth + RBAC + scaffolding).
# CGO disabled -> static binary, runs on alpine/scratch without gcc.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---- Runtime stage ----
FROM alpine:3.20
# ca-certificates for any outbound TLS; tzdata for correct timezone handling.
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/server ./server
EXPOSE 8080
ENTRYPOINT ["./server"]
