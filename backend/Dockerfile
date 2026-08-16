FROM golang:1.26-alpine AS build-base

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

FROM build-base AS iam-builder
RUN go build -trimpath -o /out/service ./cmd/api

FROM build-base AS department-builder
RUN go build -trimpath -o /out/service ./cmd/department

FROM build-base AS gateway-builder
RUN go build -trimpath -o /out/service ./cmd/gateway

FROM alpine:3.22 AS runtime
RUN apk add --no-cache ca-certificates \
    && addgroup -S app \
    && adduser -S app -G app
WORKDIR /app
USER app

FROM runtime AS iam
COPY --from=iam-builder /out/service ./service
EXPOSE 8080 50051
ENTRYPOINT ["./service"]

FROM runtime AS department
COPY --from=department-builder /out/service ./service
EXPOSE 8080 50051
ENTRYPOINT ["./service"]

FROM runtime AS gateway
COPY --from=gateway-builder /out/service ./service
EXPOSE 8080
ENTRYPOINT ["./service"]
