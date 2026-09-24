FROM golang:alpine AS builder
WORKDIR /build
COPY go.mod go.sum* ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /app/gentle-mesh ./cmd/gentle-mesh

FROM alpine:3.20
RUN apk --no-cache add ca-certificates curl
WORKDIR /app
COPY --from=builder /app/gentle-mesh /usr/local/bin/gentle-mesh
ENTRYPOINT ["gentle-mesh"]
CMD ["server", "-addr", ":8080"]
