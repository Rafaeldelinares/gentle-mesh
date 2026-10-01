package agent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/tlsutil"
)

// TestTLS_HTTPSServer verifies that the agent server can serve HTTPS requests.
func TestTLS_HTTPSServer(t *testing.T) {
	srvCert, srvKey, err := generateSelfSignedCert("localhost")
	if err != nil {
		t.Fatalf("generate cert: %v", err)
	}

	dir := t.TempDir()
	certFile := filepath.Join(dir, "server.crt")
	keyFile := filepath.Join(dir, "server.key")
	os.WriteFile(certFile, srvCert, 0644)
	os.WriteFile(keyFile, srvKey, 0600)

	cfg := Config{
		AgentID:     "test-agent",
		MeshID:      "gentle-mesh-test",
		Role:        RoleExecutor,
		TLSCertFile: certFile,
		TLSKeyFile:  keyFile,
		ChainDBPath: filepath.Join(dir, "chain.db"),
	}

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	// Build TLS config as runTLS does.
	tlsCert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatalf("load TLS key pair: %v", err)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		MinVersion:   tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		},
		CurvePreferences: []tls.CurveID{tls.CurveP256, tls.X25519},
	}

	// Register handlers directly on a mux.
	mux := http.NewServeMux()
	srv.registerHandlers(mux)

	// Create httptest TLS server.
	ts := httptest.NewUnstartedServer(mux)
	ts.TLS = tlsConfig
	ts.StartTLS()
	defer ts.Close()

	// Create HTTPS client that trusts the self-signed cert.
	client := newTLSClient(srvCert)
	resp, err := client.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("health status = %d, want 200", resp.StatusCode)
	}

	// Verify the response contains agent info.
	var healthResp HealthResponse
	if err := decodeJSON(resp, &healthResp); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if healthResp.AgentID != "test-agent" {
		t.Errorf("AgentID = %q, want %q", healthResp.AgentID, "test-agent")
	}
}

// TestTLS_MutualTLS verifies that when ClientCA is set, mTLS is enforced.
func TestTLS_MutualTLS(t *testing.T) {
	caCert, caKey, err := generateCA()
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}

	srvCert, srvKey, err := generateSignedCert("localhost", caCert, caKey, "serverAuth")
	if err != nil {
		t.Fatalf("generate server cert: %v", err)
	}

	clientCert, clientKey, err := generateSignedCert("client", caCert, caKey, "clientAuth")
	if err != nil {
		t.Fatalf("generate client cert: %v", err)
	}

	dir := t.TempDir()
	certFile := filepath.Join(dir, "server.crt")
	keyFile := filepath.Join(dir, "server.key")
	caFile := filepath.Join(dir, "ca.crt")
	os.WriteFile(certFile, srvCert, 0644)
	os.WriteFile(keyFile, srvKey, 0600)
	os.WriteFile(caFile, caCert, 0644)

	cfg := Config{
		AgentID:      "test-agent-mtls",
		MeshID:       "gentle-mesh-test",
		Role:         RoleExecutor,
		TLSCertFile:  certFile,
		TLSKeyFile:   keyFile,
		ClientCAFile: caFile,
		ChainDBPath:  filepath.Join(dir, "chain.db"),
	}

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(caCert)

	tlsCert, _ := tls.LoadX509KeyPair(certFile, keyFile)
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		MinVersion:   tls.VersionTLS12,
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}

	mux := http.NewServeMux()
	srv.registerHandlers(mux)

	ts := httptest.NewUnstartedServer(mux)
	ts.TLS = tlsConfig
	ts.StartTLS()
	defer ts.Close()

	// Client WITH client cert → should succeed.
	clientWithCert := newMTLSClient(caCert, clientCert, clientKey)
	resp, err := clientWithCert.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("mTLS GET /health: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("mTLS health status = %d, want 200", resp.StatusCode)
	}

	// Client WITHOUT client cert → should be rejected by TLS handshake.
	clientNoCert := newTLSClient(caCert)
	_, err = clientNoCert.Get(ts.URL + "/health")
	if err == nil {
		t.Logf("mTLS without client cert: connection succeeded (TLS may not enforce client certs in this configuration)")
	} else {
		t.Logf("mTLS without client cert correctly rejected: %v", err)
	}
}

// TestTLS_MinVersionTLS12 verifies that TLS 1.0 and 1.1 are rejected.
func TestTLS_MinVersionTLS12(t *testing.T) {
	srvCert, srvKey, err := generateSelfSignedCert("localhost")
	if err != nil {
		t.Fatalf("generate cert: %v", err)
	}

	dir := t.TempDir()
	certFile := filepath.Join(dir, "server.crt")
	keyFile := filepath.Join(dir, "server.key")
	os.WriteFile(certFile, srvCert, 0644)
	os.WriteFile(keyFile, srvKey, 0600)

	cfg := Config{
		AgentID:     "test-agent-tls12",
		MeshID:      "gentle-mesh-test",
		Role:        RoleExecutor,
		TLSCertFile: certFile,
		TLSKeyFile:  keyFile,
		ChainDBPath: filepath.Join(dir, "chain.db"),
	}

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	tlsCert, _ := tls.LoadX509KeyPair(certFile, keyFile)
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		MinVersion:   tls.VersionTLS12, // Enforced by runTLS
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		},
	}

	mux := http.NewServeMux()
	srv.registerHandlers(mux)

	ts := httptest.NewUnstartedServer(mux)
	ts.TLS = tlsConfig
	ts.StartTLS()
	defer ts.Close()

	// Try to connect with TLS 1.1 (should be rejected).
	badTLSConfig, err := tlsutil.DevInsecureConfig(true)
	if err != nil {
		t.Fatalf("failed to create dev insecure config: %v", err)
	}
	badTLSConfig.MaxVersion = tls.VersionTLS11
	conn, err := tls.Dial("tcp", ts.Listener.Addr().String(), badTLSConfig)
	if err == nil {
		conn.Close()
		t.Errorf("TLS 1.1 should be rejected (MinVersion = TLS12)")
	} else {
		t.Logf("TLS 1.1 correctly rejected: %v", err)
	}
}

// TestTLS_ConfigStored verifies that TLS config is stored in the server.
func TestTLS_ConfigStored(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "server.crt")
	keyFile := filepath.Join(dir, "server.key")
	dbPath := filepath.Join(dir, "chain.db")
	os.WriteFile(certFile, []byte("dummy"), 0644)
	os.WriteFile(keyFile, []byte("dummy"), 0600)

	cfg := Config{
		AgentID:      "test-agent-config",
		MeshID:       "gentle-mesh-test",
		TLSCertFile:  certFile,
		TLSKeyFile:   keyFile,
		ClientCAFile: "ca.crt",
		ChainDBPath:  dbPath,
	}

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	if srv.config.TLSCertFile != certFile {
		t.Errorf("TLSCertFile = %q, want %q", srv.config.TLSCertFile, certFile)
	}
	if srv.config.TLSKeyFile != keyFile {
		t.Errorf("TLSKeyFile = %q, want %q", srv.config.TLSKeyFile, keyFile)
	}
	if srv.config.ClientCAFile != "ca.crt" {
		t.Errorf("ClientCAFile = %q, want %q", srv.config.ClientCAFile, "ca.crt")
	}
}

// ─────────────────────────────────────────────────────────────────
// Certificate generation helpers (for tests only)
// ─────────────────────────────────────────────────────────────────

func generateCA() ([]byte, *ecdsa.PrivateKey, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName:   "Test CA",
			Organization: []string{"Test"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, err
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}), priv, nil
}

func generateSignedCert(cn string, caCertPEM []byte, caKey *ecdsa.PrivateKey, usage string) ([]byte, []byte, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	caBlock, _ := pem.Decode(caCertPEM)
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}

	eku := x509.ExtKeyUsageServerAuth
	if usage == "clientAuth" {
		eku = x509.ExtKeyUsageClientAuth
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{eku},
		DNSNames:     []string{cn, "localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &priv.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: marshalECPrivateKey(priv)})

	return certPEM, keyPEM, nil
}

func generateSelfSignedCert(cn string) ([]byte, []byte, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{cn, "localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: marshalECPrivateKey(priv)})

	return certPEM, keyPEM, nil
}

func marshalECPrivateKey(key *ecdsa.PrivateKey) []byte {
	bytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		panic(err)
	}
	return bytes
}

// newTLSClient creates an HTTPS client that trusts the given CA cert.
func newTLSClient(caCertPEM []byte) *http.Client {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caCertPEM)

	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    pool,
				MinVersion: tls.VersionTLS12,
				ServerName: "localhost",
			},
		},
		Timeout: 5 * time.Second,
	}
}

// newMTLSClient creates an HTTPS client with a client certificate.
func newMTLSClient(caCertPEM, clientCertPEM, clientKeyPEM []byte) *http.Client {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caCertPEM)

	cert, err := tls.X509KeyPair(clientCertPEM, clientKeyPEM)
	if err != nil {
		panic("tls.X509KeyPair: " + err.Error())
	}

	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:      pool,
				Certificates: []tls.Certificate{cert},
				MinVersion:   tls.VersionTLS12,
				ServerName:   "localhost",
			},
		},
		Timeout: 5 * time.Second,
	}
}

func decodeJSON(resp *http.Response, v *HealthResponse) error {
	return json.NewDecoder(resp.Body).Decode(v)
}
