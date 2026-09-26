#!/usr/bin/env bash
# gen-certs.sh — Generate self-signed TLS certificates for RFC-002 development.
# Usage: ./gen-certs.sh [output-dir]
# Produces:
#   ca.crt          — Root CA certificate (self-signed)
#   ca.key          — CA private key (mode 0600)
#   server.crt      — Server certificate signed by CA
#   server.key      — Server private key (mode 0600)
#   client.crt      — Client certificate signed by CA (for mTLS)
#   client.key      — Client private key (mode 0600)
set -euo pipefail

OUT_DIR="${1:-$(dirname "$0")}"
mkdir -p "$OUT_DIR"

# ─── Configuration ───────────────────────────────────────────────────
COUNTRY="US"
STATE="Development"
LOC="Internet"
ORG="Gentleman Programming"
CA_NAME="Gentle Mesh Development CA"
SERVER_NAME="agent-server"
CLIENT_NAME="agent-client"
DAYS=3650  # 10 years for CA, 1 year for leaf certs

# ─── Helper: generate EC private key ────────────────────────────────
gen_key() {
    local out="$1"
    openssl ecparam -name prime256v1 -genkey -noout -out "$out"
    chmod 0600 "$out"
}

# ─── Helper: create self-signed CA ─────────────────────────────────
create_ca() {
    local key="$OUT_DIR/ca.key"
    local cert="$OUT_DIR/ca.crt"

    echo "[TLS] Generating CA key..."
    gen_key "$key"

    echo "[TLS] Generating CA certificate..."
    openssl req -x509 -new -nodes -key "$key" \
        -sha256 -days "$DAYS" \
        -out "$cert" \
        -subj "/C=$COUNTRY/ST=$STATE/L=$LOC/O=$ORG/CN=$CA_NAME" \
        -addext "basicConstraints=critical,CA:TRUE,pathlen:0" \
        -addext "keyUsage=critical,keyCertSign,cRLSign,digitalSignature" \
        -addext "subjectKeyIdentifier=hash"

    echo "[TLS] CA ready: $cert (SHA-256 $(openssl x509 -noout -fingerprint -sha256 -in "$cert" | cut -d= -f2))"
}

# ─── Helper: create signed leaf certificate ───────────────────────
create_leaf() {
    local name="$1"       # e.g. "server"
    local usage="$2"     # "serverAuth" or "clientAuth"
    local alt_names="$3" # comma-separated SANs

    local key="$OUT_DIR/${name}.key"
    local csr="$OUT_DIR/${name}.csr"
    local cert="$OUT_DIR/${name}.crt"

    echo "[TLS] Generating $name key..."
    gen_key "$key"

    echo "[TLS] Generating $name CSR..."
    openssl req -new -key "$key" -out "$csr" \
        -subj "/C=$COUNTRY/ST=$STATE/L=$LOC/O=$ORG/CN=$name" \
        -addext "subjectAltName=DNS:localhost,DNS:$name,IP:127.0.0.1,IP:::1"

    echo "[TLS] Signing $name certificate..."
    openssl x509 -req -in "$csr" \
        -CA "$OUT_DIR/ca.crt" -CAkey "$OUT_DIR/ca.key" \
        -CAcreateserial -out "$cert" \
        -days 365 -sha256 \
        -extfile <(cat <<EOF
basicConstraints=CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=$usage
subjectKeyIdentifier=hash
authorityKeyIdentifier=keyid:always
EOF
)

    # Clean up CSR and serial file.
    rm -f "$csr" "$OUT_DIR/${name}.srl"

    chmod 0600 "$key"
    echo "[TLS] $name cert ready: $cert"
}

# ─── Main ──────────────────────────────────────────────────────────
echo "[TLS] Generating TLS certificates in: $OUT_DIR"
echo "[TLS] =============================================="

create_ca
create_leaf "server" "serverAuth" "localhost,agent-server,agent-a,agent-b"
create_leaf "client" "clientAuth" "localhost,agent-a,agent-b"

echo ""
echo "[TLS] =============================================="
echo "[TLS] Certificate generation complete."
echo "[TLS]"
echo "[TLS] CA cert (add to trust store): $OUT_DIR/ca.crt"
echo "[TLS] Server cert: $OUT_DIR/server.crt"
echo "[TLS] Client cert: $OUT_DIR/client.crt"
echo ""
echo "[TLS] In Docker, mount the certs directory and set:"
echo "[TLS]   TLS_CERT_FILE=/certs/server.crt"
echo "[TLS]   TLS_KEY_FILE=/certs/server.key"
echo "[TLS]   CA_CERT_FILE=/certs/ca.crt"
echo ""
echo "[TLS] For local testing, add CA to trust store:"
echo "[TLS]   sudo cp $OUT_DIR/ca.crt /usr/local/share/ca-certificates/gentle-mesh-dev.crt"
echo "[TLS]   sudo update-ca-certificates"
