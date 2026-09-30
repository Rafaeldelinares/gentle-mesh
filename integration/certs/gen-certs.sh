#!/bin/sh
# gen-certs.sh — Generate self-signed TLS certificates for RFC-002 development.
# POSIX sh (no bash, no process substitution).
# Usage: ./gen-certs.sh [output-dir]
set -e

OUT_DIR="${1:-$(dirname "$0")}"
mkdir -p "$OUT_DIR"

# ─── Configuration ───────────────────────────────────────────────────
CA_NAME="Gentle Mesh Development CA"
SERVER_NAME="server"
CLIENT_NAME="client"
DAYS_CA=3650   # 10 years for CA
DAYS_LEAF=365  # 1 year for leaf certs

# ─── Helper: generate EC private key ────────────────────────────────
gen_key() {
    local_out="$1"
    openssl ecparam -name prime256v1 -genkey -noout -out "$local_out"
    chmod 0600 "$local_out"
}

# ─── Helper: create self-signed CA ─────────────────────────────────
create_ca() {
    key="$OUT_DIR/ca.key"
    cert="$OUT_DIR/ca.crt"

    echo "[TLS] Generating CA key..."
    gen_key "$key"

    echo "[TLS] Generating CA certificate..."
    openssl req -x509 -new -nodes -key "$key" \
        -sha256 -days "$DAYS_CA" \
        -out "$cert" \
        -subj "/C=US/ST=Development/L=Internet/O=Gentleman Programming/CN=$CA_NAME" \
        -addext "basicConstraints=critical,CA:TRUE,pathlen:0" \
        -addext "keyUsage=critical,keyCertSign,cRLSign,digitalSignature" \
        -addext "subjectKeyIdentifier=hash"

    chmod 0644 "$cert"
    echo "[TLS] CA ready: $cert"
}

# ─── Helper: create signed leaf certificate (no process substitution) ─
create_leaf() {
    name="$1"       # e.g. "server"
    usage="$2"      # "serverAuth" or "clientAuth"

    key="$OUT_DIR/${name}.key"
    csr="$OUT_DIR/${name}.csr"
    cert="$OUT_DIR/${name}.crt"
    extfile="$OUT_DIR/${name}.ext"

    echo "[TLS] Generating $name key..."
    gen_key "$key"

    echo "[TLS] Generating $name CSR..."
    openssl req -new -key "$key" -out "$csr" \
        -subj "/C=US/ST=Development/L=Internet/O=Gentleman Programming/CN=$name" \
        -addext "subjectAltName=DNS:localhost,DNS:$name,DNS:agent-a,DNS:agent-b,DNS:agent-c,IP:127.0.0.1,IP:::1"

    # Write extension file to disk (avoids process substitution).
    cat > "$extfile" << EOFEXT
basicConstraints=CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=$usage
subjectKeyIdentifier=hash
authorityKeyIdentifier=keyid:always
subjectAltName=DNS:localhost,DNS:$name,DNS:agent-a,DNS:agent-b,DNS:agent-c,IP:127.0.0.1,IP:::1
EOFEXT

    echo "[TLS] Signing $name certificate..."
    openssl x509 -req -in "$csr" \
        -CA "$OUT_DIR/ca.crt" -CAkey "$OUT_DIR/ca.key" \
        -CAcreateserial -out "$cert" \
        -days "$DAYS_LEAF" -sha256 \
        -extfile "$extfile"

    # Clean up CSR and serial file.
    rm -f "$csr" "$extfile" "$OUT_DIR/${name}.srl"

    chmod 0644 "$cert"
    echo "[TLS] $name cert ready: $cert"
}

# ─── Main ──────────────────────────────────────────────────────────
echo "[TLS] Generating TLS certificates in: $OUT_DIR"
echo "[TLS] =============================================="

create_ca
create_leaf "server" "serverAuth"
create_leaf "client" "clientAuth"

echo ""
echo "[TLS] =============================================="
echo "[TLS] Certificate generation complete."
echo "[TLS]"
echo "[TLS] CA cert:       $OUT_DIR/ca.crt     (add to trust store)"
echo "[TLS] Server cert:   $OUT_DIR/server.crt (signed by CA, SAN=agent-a,agent-b)"
echo "[TLS] Client cert:   $OUT_DIR/client.crt (for mTLS)"
echo "[TLS] Private keys:   $OUT_DIR/*.key      (mode 0600)"
echo ""
echo "[TLS] In Docker, mount the certs directory and set:"
echo "[TLS]   --tls-cert /certs/server.crt"
echo "[TLS]   --tls-key  /certs/server.key"
echo ""
echo "[TLS] For local testing, add CA to trust store:"
echo "[TLS]   sudo cp $OUT_DIR/ca.crt /usr/local/share/ca-certificates/gentle-mesh-dev.crt"
echo "[TLS]   sudo update-ca-certificates"
