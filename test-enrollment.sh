#!/bin/bash
set -e

# Test enrollment flow in Docker

TLS_DIR="$(pwd)/tls-test"
DATA_DIR="$(pwd)/data"
mkdir -p "$TLS_DIR" "$DATA_DIR"

echo "=== 1. Inicializando TLS ==="
docker run --rm -v "$TLS_DIR:/tls" gentle-mesh-coordinator:latest server -tls-init -tls-dir /tls

echo ""
echo "=== 2. Iniciando coordinator con TLS ==="
docker compose -f docker-compose.tls-test.yml up -d coordinator
sleep 3

echo ""
echo "=== 3. Verificando coordinator ==="
curl -k https://localhost:8443/healthz 2>/dev/null | jq .

echo ""
echo "=== 4. Copiando TLS al host para CLI ==="
cp "$TLS_DIR/gentle-mesh-ca.pem" ./ca.pem

echo ""
echo "=== 5. Generando enrollment token ==="
docker run --rm -v "$DATA_DIR:/data" -v "$TLS_DIR:/tls" gentle-mesh-coordinator:latest gen-token -db-path /data/mesh.db

echo ""
echo "=== 6. Listando tokens ==="
docker run --rm -v "$DATA_DIR:/data" gentle-mesh-coordinator:latest token-list -db-path /data/mesh.db

echo ""
echo "=== 7. Obteniendo token ==="
TOKEN=$(docker run --rm -v "$DATA_DIR:/data" gentle-mesh-coordinator:latest token-list -db-path /data/mesh.db 2>/dev/null | grep "Token:" | awk '{print $2}' | head -1)
echo "Token: $TOKEN"

echo ""
echo "=== 8. Verificando enrollment endpoint ==="
curl -k -X POST https://localhost:8443/v1/certs/enroll \
  -H "Content-Type: application/json" \
  -d "{\"token\":\"$TOKEN\",\"csr\":\"test\",\"node_id\":\"test-node\"}" 2>/dev/null | jq .

echo ""
echo "=== 9. Agregando worker con enrollment automático ==="
# Modificar docker-compose para usar token
cat > docker-compose.tls-test.yml << 'EOF'
services:
  coordinator:
    build:
      context: .
      dockerfile: Dockerfile
    command: ["server", "-addr", ":8443", "-tls", "-tls-dir", "/tls", "-db-path", "/data/mesh.db", "-workspace", "/workspace"]
    volumes:
      - ./tls-test:/tls
      - ./data:/data
    ports:
      - "8443:8443"
    healthcheck:
      test: ["CMD", "curl", "-kf", "https://localhost:8443/healthz"]
      interval: 5s
      timeout: 3s
      retries: 5
      start_period: 5s
    networks:
      - gentle-mesh-net

  worker-alpha:
    build:
      context: .
      dockerfile: Dockerfile
    # TODO: agregar -join-token cuando esté soportado en el worker de Docker
    command: ["worker", "-coordinator", "https://coordinator:8443", "-node-id", "worker-alpha", "-endpoint", "http://worker-alpha:8081", "-agents", "worker", "-tags", "go,fast", "-concurrency", "2", "-insecure-skip-tls-verify"]
    depends_on:
      coordinator:
        condition: service_healthy
    networks:
      - gentle-mesh-net

networks:
  gentle-mesh-net:
    driver: bridge
EOF

docker compose -f docker-compose.tls-test.yml up -d
sleep 5

echo ""
echo "=== 10. Verificando workers ==="
curl -k https://localhost:8443/v1/mesh/nodes 2>/dev/null | jq .

echo ""
echo "=== Cleanup ==="
docker compose -f docker-compose.tls-test.yml down 2>/dev/null || true
