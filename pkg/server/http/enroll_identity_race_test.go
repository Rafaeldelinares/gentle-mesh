package http_test

import (
	"bytes"
	"encoding/json"
	"sync"
	"testing"

	"github.com/gentleman-programming/gentle-mesh/pkg/pki"
)

// TestConcurrentEnrollmentsOfOneIdentityMustNotBothSucceed covers the race in
// the enrollment guard: GetCert and IssueCert are two separate operations, so
// two simultaneous requests for the same node_id could both observe "no
// certificate" and both get a CA-signed certificate.
//
// The loser's certificate is worse than a duplicate: the winner's IssueCert
// upserts over node_id, so the loser ends up in neither node_certs nor the
// revocation ledger. Nothing can revoke it, it is valid for a year, and its
// holder can present it at a TLS handshake.
func TestConcurrentEnrollmentsOfOneIdentityMustNotBothSucceed(t *testing.T) {
	ca, tlsDir, tokenStore, certStore, _ := setupMTLSTestEnvironment(t)
	_, addr := startTestServer(t, enrollServerConfig(t, ca, tlsDir, tokenStore, certStore))
	srvURL := "https://" + addr

	const rounds = 10

	for round := 0; round < rounds; round++ {
		nodeID := "race-node-" + itoa(round)

		var mu sync.Mutex
		var wg sync.WaitGroup
		start := make(chan struct{})
		accepted := make([]string, 0, 2)

		for _, token := range []string{"a", "b"} {
			tok := nodeID + "-" + token
			makeToken(t, tokenStore, tok, 1)

			wg.Add(1)
			go func(tok string) {
				defer wg.Done()
				csr, err := pki.GenerateCSR(nodeID)
				if err != nil {
					return
				}
				payload, _ := json.Marshal(map[string]string{
					"token":   tok,
					"csr":     csr.CSRPEM,
					"node_id": nodeID,
				})
				client := createClient(t, ca, nil)
				<-start
				resp, err := client.Post(srvURL+"/v1/certs/enroll", "application/json", bytes.NewReader(payload))
				if err != nil {
					return
				}
				defer resp.Body.Close()
				if resp.StatusCode == 200 {
					mu.Lock()
					accepted = append(accepted, tok)
					mu.Unlock()
				}
			}(tok)
		}

		close(start)
		wg.Wait()

		if len(accepted) > 1 {
			t.Fatalf("identity %q received %d certificates from concurrent enrollments (%v); "+
				"the rejected one is in neither the certificate table nor the revocation ledger, "+
				"so it can never be revoked", nodeID, len(accepted), accepted)
		}
	}
}

// itoa avoids pulling strconv in for a loop counter that only needs digits.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
