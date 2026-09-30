package tlsutil_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/tlsutil"
)

func TestDevInsecureConfig_ExplicitFlagRequired(t *testing.T) {
	t.Setenv("GENTLE_ENV", "development")

	cfg, err := tlsutil.DevInsecureConfig(false)
	if err == nil {
		t.Fatal("expected error when explicitFlag is false, got nil")
	}
	if !errors.Is(err, tlsutil.ErrExplicitFlagRequired) {
		t.Fatalf("expected ErrExplicitFlagRequired, got: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil config, got: %+v", cfg)
	}
}

func TestDevInsecureConfig_ProhibitedInProduction(t *testing.T) {
	prodEnvs := []string{"production", "PRODUCTION", "prod", "PROD", " Production "}
	for _, env := range prodEnvs {
		t.Run(env, func(t *testing.T) {
			t.Setenv("GENTLE_ENV", env)

			cfg, err := tlsutil.DevInsecureConfig(true)
			if err == nil {
				t.Fatalf("expected error in production env %q, got nil", env)
			}
			if !errors.Is(err, tlsutil.ErrInsecureInProduction) {
				t.Fatalf("expected ErrInsecureInProduction, got: %v", err)
			}
			if cfg != nil {
				t.Fatalf("expected nil config in production, got: %+v", cfg)
			}
		})
	}
}

func TestDevInsecureConfig_WarningEmitted(t *testing.T) {
	t.Setenv("GENTLE_ENV", "development")

	var buf bytes.Buffer
	cfg, err := tlsutil.DevInsecureConfigWithOutput(true, &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if !cfg.InsecureSkipVerify {
		t.Error("expected InsecureSkipVerify to be true")
	}

	warning := buf.String()
	if !strings.Contains(warning, "WARNING") || !strings.Contains(warning, "InsecureSkipVerify") {
		t.Fatalf("expected prominent warning message, got: %q", warning)
	}
}

func TestApplyDevInsecure(t *testing.T) {
	t.Run("nil config returns error", func(t *testing.T) {
		t.Setenv("GENTLE_ENV", "development")
		err := tlsutil.ApplyDevInsecure(nil, true)
		if err == nil {
			t.Fatal("expected error on nil config, got nil")
		}
	})

	t.Run("explicit flag required", func(t *testing.T) {
		t.Setenv("GENTLE_ENV", "development")
		cfg := &tls.Config{}
		err := tlsutil.ApplyDevInsecure(cfg, false)
		if !errors.Is(err, tlsutil.ErrExplicitFlagRequired) {
			t.Fatalf("expected ErrExplicitFlagRequired, got: %v", err)
		}
		if cfg.InsecureSkipVerify {
			t.Error("InsecureSkipVerify should remain false")
		}
	})

	t.Run("prohibited in production", func(t *testing.T) {
		t.Setenv("GENTLE_ENV", "production")
		cfg := &tls.Config{}
		err := tlsutil.ApplyDevInsecure(cfg, true)
		if !errors.Is(err, tlsutil.ErrInsecureInProduction) {
			t.Fatalf("expected ErrInsecureInProduction, got: %v", err)
		}
		if cfg.InsecureSkipVerify {
			t.Error("InsecureSkipVerify should remain false in production")
		}
	})

	t.Run("success in development", func(t *testing.T) {
		t.Setenv("GENTLE_ENV", "development")
		cfg := &tls.Config{}
		err := tlsutil.ApplyDevInsecure(cfg, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !cfg.InsecureSkipVerify {
			t.Error("expected InsecureSkipVerify to be true")
		}
	})
}

func TestPinnedBootstrapConfig(t *testing.T) {
	t.Run("empty server name returns error", func(t *testing.T) {
		cfg, err := tlsutil.PinnedBootstrapConfig("", "sha256:11223344")
		if !errors.Is(err, tlsutil.ErrEmptyServerName) {
			t.Fatalf("expected ErrEmptyServerName, got: %v", err)
		}
		if cfg != nil {
			t.Fatalf("expected nil config, got: %+v", cfg)
		}

		cfg, err = tlsutil.PinnedBootstrapConfig("   ", "sha256:11223344")
		if !errors.Is(err, tlsutil.ErrEmptyServerName) {
			t.Fatalf("expected ErrEmptyServerName for whitespace, got: %v", err)
		}
		if cfg != nil {
			t.Fatalf("expected nil config, got: %+v", cfg)
		}

		cfg, err = tlsutil.PinnedBootstrapConfig(":8443", "sha256:11223344")
		if !errors.Is(err, tlsutil.ErrEmptyServerName) {
			t.Fatalf("expected ErrEmptyServerName for empty host with port, got: %v", err)
		}
		if cfg != nil {
			t.Fatalf("expected nil config, got: %+v", cfg)
		}
	})

	t.Run("empty hash returns error", func(t *testing.T) {
		cfg, err := tlsutil.PinnedBootstrapConfig("localhost", "")
		if !errors.Is(err, tlsutil.ErrEmptyPinHash) {
			t.Fatalf("expected ErrEmptyPinHash, got: %v", err)
		}
		if cfg != nil {
			t.Fatalf("expected nil config, got: %+v", cfg)
		}
	})

	t.Run("invalid format returns error", func(t *testing.T) {
		cfg, err := tlsutil.PinnedBootstrapConfig("localhost", "md5:123456")
		if !errors.Is(err, tlsutil.ErrInvalidPinFormat) {
			t.Fatalf("expected ErrInvalidPinFormat, got: %v", err)
		}
		if cfg != nil {
			t.Fatalf("expected nil config, got: %+v", cfg)
		}
	})

	t.Run("allowed in production without warning", func(t *testing.T) {
		t.Setenv("GENTLE_ENV", "production")
		var buf bytes.Buffer
		oldWarn := tlsutil.WarnWriter
		tlsutil.WarnWriter = &buf
		defer func() { tlsutil.WarnWriter = oldWarn }()

		cfg, err := tlsutil.PinnedBootstrapConfig("localhost", "sha256:abcd1234ef")
		if err != nil {
			t.Fatalf("expected success in production, got: %v", err)
		}
		if cfg == nil {
			t.Fatal("expected non-nil config")
		}
		if buf.Len() > 0 {
			t.Fatalf("expected no warnings for pinned bootstrap, got: %q", buf.String())
		}
	})

	t.Run("handshake verification against test server", func(t *testing.T) {
		ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer ts.Close()

		cert := ts.Certificate()
		sum := sha256.Sum256(cert.Raw)
		correctFingerprint := fmt.Sprintf("sha256:%x", sum)
		wrongFingerprint := "sha256:11223344556677889900aabbccddeeff11223344556677889900aabbccddeeff"

		// Matching pin passes (using 127.0.0.1 from ts.URL)
		cfg, err := tlsutil.PinnedBootstrapConfig("127.0.0.1", correctFingerprint)
		if err != nil {
			t.Fatalf("unexpected error creating config: %v", err)
		}
		client := &http.Client{
			Transport: &http.Transport{TLSClientConfig: cfg},
		}
		resp, err := client.Get(ts.URL)
		if err != nil {
			t.Fatalf("expected request with matching pin to succeed, got: %v", err)
		}
		resp.Body.Close()

		// Mismatched pin fails during handshake
		badCfg, err := tlsutil.PinnedBootstrapConfig("127.0.0.1", wrongFingerprint)
		if err != nil {
			t.Fatalf("unexpected error creating config: %v", err)
		}
		badClient := &http.Client{
			Transport: &http.Transport{TLSClientConfig: badCfg},
		}
		_, err = badClient.Get(ts.URL)
		if err == nil {
			t.Fatal("expected request with wrong pin to fail during handshake, got nil error")
		}
	})

	t.Run("rejects attacker leaf cert with anchor present in chain", func(t *testing.T) {
		// Generate legitimate root (anchor)
		anchorKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		anchorTemplate := &x509.Certificate{
			SerialNumber:          big.NewInt(1),
			Subject:               pkix.Name{CommonName: "Legit Root CA"},
			NotBefore:             time.Now().Add(-1 * time.Hour),
			NotAfter:              time.Now().Add(24 * time.Hour),
			IsCA:                  true,
			KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
			BasicConstraintsValid: true,
		}
		anchorDER, _ := x509.CreateCertificate(rand.Reader, anchorTemplate, anchorTemplate, &anchorKey.PublicKey, anchorKey)
		anchorCert, _ := x509.ParseCertificate(anchorDER)
		anchorHash := fmt.Sprintf("sha256:%x", sha256.Sum256(anchorCert.Raw))

		// Attacker generates own key and leaf cert (not signed by anchor)
		attackerKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		attackerTemplate := &x509.Certificate{
			SerialNumber: big.NewInt(2),
			Subject:      pkix.Name{CommonName: "localhost"},
			NotBefore:    time.Now().Add(-1 * time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			DNSNames:     []string{"localhost"},
			IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		}
		attackerDER, _ := x509.CreateCertificate(rand.Reader, attackerTemplate, attackerTemplate, &attackerKey.PublicKey, attackerKey)

		// Attacker serves attackerDER as leaf, but includes anchorDER in chain
		attackerServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		attackerServer.TLS = &tls.Config{
			Certificates: []tls.Certificate{
				{
					Certificate: [][]byte{attackerDER, anchorDER},
					PrivateKey:  attackerKey,
				},
			},
		}
		attackerServer.StartTLS()
		defer attackerServer.Close()

		cfg, err := tlsutil.PinnedBootstrapConfigWithServerName("localhost", anchorHash)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
		_, err = client.Get(attackerServer.URL)
		if err == nil {
			t.Fatal("expected handshake rejection when leaf is not signed by anchor, got nil")
		}
	})

	t.Run("IP address verification in handshake", func(t *testing.T) {
		// Generate anchor CA
		caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		caTemplate := &x509.Certificate{
			SerialNumber:          big.NewInt(100),
			Subject:               pkix.Name{CommonName: "IP Test CA"},
			NotBefore:             time.Now().Add(-1 * time.Hour),
			NotAfter:              time.Now().Add(24 * time.Hour),
			IsCA:                  true,
			KeyUsage:              x509.KeyUsageCertSign,
			BasicConstraintsValid: true,
		}
		caDER, _ := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
		caCert, _ := x509.ParseCertificate(caDER)
		caHash := fmt.Sprintf("sha256:%x", sha256.Sum256(caCert.Raw))

		// Server certificate valid for 127.0.0.1
		serverKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		serverTemplate := &x509.Certificate{
			SerialNumber: big.NewInt(101),
			Subject:      pkix.Name{CommonName: "Server"},
			NotBefore:    time.Now().Add(-1 * time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		}
		serverDER, _ := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)

		ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		ts.TLS = &tls.Config{
			Certificates: []tls.Certificate{
				{
					Certificate: [][]byte{serverDER, caDER},
					PrivateKey:  serverKey,
				},
			},
		}
		ts.StartTLS()
		defer ts.Close()

		// 1. Correct IP (127.0.0.1) succeeds
		cfgValid, err := tlsutil.PinnedBootstrapConfig("127.0.0.1", caHash)
		if err != nil {
			t.Fatalf("unexpected error creating config: %v", err)
		}
		clientValid := &http.Client{Transport: &http.Transport{TLSClientConfig: cfgValid}}
		resp, err := clientValid.Get(ts.URL)
		if err != nil {
			t.Fatalf("expected request with matching IP to succeed, got: %v", err)
		}
		resp.Body.Close()

		// 2. Different IP (192.168.1.99) fails handshake
		cfgWrongIP, err := tlsutil.PinnedBootstrapConfig("192.168.1.99", caHash)
		if err != nil {
			t.Fatalf("unexpected error creating config: %v", err)
		}
		clientWrongIP := &http.Client{Transport: &http.Transport{TLSClientConfig: cfgWrongIP}}
		_, err = clientWrongIP.Get(ts.URL)
		if err == nil {
			t.Fatal("expected handshake failure when connecting with mismatched IP, got nil")
		}
	})

	t.Run("DNS hostname verification in handshake", func(t *testing.T) {
		caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		caTemplate := &x509.Certificate{
			SerialNumber:          big.NewInt(200),
			Subject:               pkix.Name{CommonName: "DNS Test CA"},
			NotBefore:             time.Now().Add(-1 * time.Hour),
			NotAfter:              time.Now().Add(24 * time.Hour),
			IsCA:                  true,
			KeyUsage:              x509.KeyUsageCertSign,
			BasicConstraintsValid: true,
		}
		caDER, _ := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
		caCert, _ := x509.ParseCertificate(caDER)
		caHash := fmt.Sprintf("sha256:%x", sha256.Sum256(caCert.Raw))

		// Server certificate valid for localhost
		serverKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		serverTemplate := &x509.Certificate{
			SerialNumber: big.NewInt(201),
			Subject:      pkix.Name{CommonName: "Server"},
			NotBefore:    time.Now().Add(-1 * time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			DNSNames:     []string{"localhost"},
		}
		serverDER, _ := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)

		ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		ts.TLS = &tls.Config{
			Certificates: []tls.Certificate{
				{
					Certificate: [][]byte{serverDER, caDER},
					PrivateKey:  serverKey,
				},
			},
		}
		ts.StartTLS()
		defer ts.Close()

		localhostURL := strings.Replace(ts.URL, "127.0.0.1", "localhost", 1)

		// 1. Correct DNS hostname (localhost) succeeds
		cfgValid, err := tlsutil.PinnedBootstrapConfig("localhost", caHash)
		if err != nil {
			t.Fatalf("unexpected error creating config: %v", err)
		}
		clientValid := &http.Client{Transport: &http.Transport{TLSClientConfig: cfgValid}}
		resp, err := clientValid.Get(localhostURL)
		if err != nil {
			t.Fatalf("expected request with matching DNS to succeed, got: %v", err)
		}
		resp.Body.Close()

		// 2. Mismatched DNS hostname fails handshake
		cfgWrongDNS, err := tlsutil.PinnedBootstrapConfig("other.server.mesh", caHash)
		if err != nil {
			t.Fatalf("unexpected error creating config: %v", err)
		}
		clientWrongDNS := &http.Client{Transport: &http.Transport{TLSClientConfig: cfgWrongDNS}}
		_, err = clientWrongDNS.Get(localhostURL)
		if err == nil {
			t.Fatal("expected handshake failure when connecting with mismatched DNS, got nil")
		}
	})
}
