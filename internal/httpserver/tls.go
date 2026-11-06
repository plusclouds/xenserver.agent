package httpserver

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	selfSignedValidity = 365 * 24 * time.Hour
	// renewBefore regenerates a self-signed cert that expires within this window.
	renewBefore = 30 * 24 * time.Hour
)

// LoadOrCreateCert returns the TLS certificate stored at certFile/keyFile.
// When the files do not exist (or a self-signed cert is close to expiry) and
// selfSigned is true, a new ECDSA P-256 self-signed certificate is generated
// and persisted to those paths so the fingerprint stays stable across restarts.
// Operator-supplied certs are never overwritten.
func LoadOrCreateCert(certFile, keyFile string, selfSigned bool, hosts []string) (tls.Certificate, bool, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err == nil {
		leaf, perr := x509.ParseCertificate(cert.Certificate[0])
		if perr != nil {
			return tls.Certificate{}, false, fmt.Errorf("parsing certificate %s: %w", certFile, perr)
		}
		// CheckSignatureFrom would reject our leaf (it is not marked as a CA),
		// so verify the self-signature directly.
		isSelf := leaf.Subject.String() == leaf.Issuer.String() &&
			leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature) == nil
		if !isSelf || time.Until(leaf.NotAfter) > renewBefore {
			return cert, isSelf, nil
		}
		if !selfSigned {
			return cert, isSelf, nil
		}
		// self-signed and expiring: fall through and regenerate
	} else if !os.IsNotExist(unwrapPathErr(err)) || !selfSigned {
		return tls.Certificate{}, false, fmt.Errorf("loading TLS key pair: %w", err)
	}

	if err := generateSelfSigned(certFile, keyFile, hosts); err != nil {
		return tls.Certificate{}, false, err
	}
	cert, err = tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return tls.Certificate{}, false, fmt.Errorf("loading generated key pair: %w", err)
	}
	return cert, true, nil
}

func unwrapPathErr(err error) error {
	if pe, ok := err.(*os.PathError); ok {
		return pe
	}
	return err
}

func generateSelfSigned(certFile, keyFile string, hosts []string) error {
	return generateSelfSignedValid(certFile, keyFile, hosts, selfSignedValidity)
}

func generateSelfSignedValid(certFile, keyFile string, hosts []string, validity time.Duration) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generating key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return fmt.Errorf("generating serial: %w", err)
	}

	hostname, _ := os.Hostname()
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "plusclouds-agent", Organization: []string{"PlusClouds"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(validity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	names := append([]string{"localhost", hostname}, hosts...)
	for _, h := range names {
		if h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	tmpl.IPAddresses = append(tmpl.IPAddresses, net.IPv4(127, 0, 0, 1), net.IPv6loopback)
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				tmpl.IPAddresses = append(tmpl.IPAddresses, ipn.IP)
			}
		}
	}
	tmpl.DNSNames = dedupeStrings(tmpl.DNSNames)
	tmpl.IPAddresses = dedupeIPs(tmpl.IPAddresses)

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("creating certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshalling key: %w", err)
	}

	for _, f := range []string{certFile, keyFile} {
		if err := os.MkdirAll(filepath.Dir(f), 0o750); err != nil {
			return fmt.Errorf("creating TLS directory: %w", err)
		}
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", certFile, err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", keyFile, err)
	}
	return nil
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func dedupeIPs(in []net.IP) []net.IP {
	seen := map[string]bool{}
	var out []net.IP
	for _, ip := range in {
		if k := ip.String(); !seen[k] {
			seen[k] = true
			out = append(out, ip)
		}
	}
	return out
}
