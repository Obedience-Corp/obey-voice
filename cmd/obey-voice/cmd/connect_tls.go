//go:build !integration

package cmd

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// connectTLSConfig always retains Go's chain, validity and hostname checks.
// A fingerprint is an additional constraint, never an alternative trust path.
func connectTLSConfig(caFile, fingerprint string) (*tls.Config, error) {
	fingerprint = strings.ToLower(strings.TrimSpace(fingerprint))
	if fingerprint != "" {
		decoded, err := hex.DecodeString(fingerprint)
		if err != nil || len(decoded) != sha256.Size {
			return nil, fmt.Errorf("--fingerprint must be a SHA-256 certificate hash (64 hexadecimal characters)")
		}
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read --ca-cert: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("load system certificate roots: %w", err)
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("--ca-cert contains no valid PEM certificates")
		}
		cfg.RootCAs = roots
	}
	if fingerprint != "" {
		cfg.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return fmt.Errorf("server presented no certificate")
			}
			sum := sha256.Sum256(state.PeerCertificates[0].Raw)
			if hex.EncodeToString(sum[:]) != fingerprint {
				return fmt.Errorf("server certificate fingerprint mismatch")
			}
			return nil
		}
	}
	return cfg, nil
}
