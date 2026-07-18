package server

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCertificateEnsureAndRotate(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "tls", "server.crt")
	keyFile := filepath.Join(dir, "tls", "server.key")
	first, err := EnsureCertificate(certFile, keyFile, []string{"router.lan"})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.SHA256) != 64 {
		t.Fatalf("fingerprint = %q", first.SHA256)
	}
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	key, ok := pair.PrivateKey.(*ecdsa.PrivateKey)
	if !ok || key.Curve.Params().Name != "P-256" {
		t.Fatalf("private key = %#v", pair.PrivateKey)
	}
	sum := sha256.Sum256(leaf.Raw)
	if first.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("fingerprint mismatch")
	}
	for _, name := range []string{"router.lan", "localhost"} {
		if err := leaf.VerifyHostname(name); err != nil {
			t.Errorf("SAN %s: %v", name, err)
		}
	}
	for _, ip := range []string{"127.0.0.1", "::1"} {
		if err := leaf.VerifyHostname(ip); err != nil {
			t.Errorf("SAN %s: %v", ip, err)
		}
	}
	if info, err := os.Stat(keyFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %v, err %v", info.Mode().Perm(), err)
	}
	second, err := EnsureCertificate(certFile, keyFile, []string{"router.lan"})
	if err != nil {
		t.Fatal(err)
	}
	if second.SHA256 != first.SHA256 {
		t.Fatalf("ensure rotated certificate")
	}
	rotated, err := RotateCertificate(certFile, keyFile, []string{"router.lan"})
	if err != nil {
		t.Fatal(err)
	}
	if rotated.SHA256 == first.SHA256 {
		t.Fatal("rotation retained fingerprint")
	}
}

func TestCertificateRotateCreatesAtNewConfiguredPaths(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "new", "server.crt"), filepath.Join(dir, "new", "server.key")
	rotated, err := RotateCertificate(certFile, keyFile, []string{"router.lan"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rotated.SHA256) != 64 {
		t.Fatalf("fingerprint = %q", rotated.SHA256)
	}
	if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
		t.Fatal(err)
	}
}

func TestCertificateRejectsBrokenExistingPair(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert"), filepath.Join(dir, "key")
	if err := os.WriteFile(certFile, []byte("broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureCertificate(certFile, keyFile, nil); err == nil {
		t.Fatal("one-sided pair accepted")
	}
	if _, err := os.Stat(keyFile); !os.IsNotExist(err) {
		t.Fatalf("key unexpectedly created: %v", err)
	}

	block, _ := pem.Decode([]byte("broken"))
	if block != nil {
		t.Fatal("fixture unexpectedly PEM")
	}
}

func TestCertificateRejectsMismatchedExistingPair(t *testing.T) {
	dir := t.TempDir()
	certA, keyA := filepath.Join(dir, "a.crt"), filepath.Join(dir, "a.key")
	certB, keyB := filepath.Join(dir, "b.crt"), filepath.Join(dir, "b.key")
	if _, err := EnsureCertificate(certA, keyA, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureCertificate(certB, keyB, nil); err != nil {
		t.Fatal(err)
	}
	keyRaw, _ := os.ReadFile(keyB)
	if err := os.WriteFile(keyA, keyRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureCertificate(certA, keyA, nil); err == nil {
		t.Fatal("mismatched pair accepted")
	}
	certRaw, _ := os.ReadFile(certA)
	if !strings.Contains(string(certRaw), "BEGIN CERTIFICATE") {
		t.Fatal("certificate was overwritten")
	}
}

func TestCertificateAcceptsIPSuppliedName(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert"), filepath.Join(dir, "key")
	if _, err := EnsureCertificate(certFile, keyFile, []string{"192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	pair, _ := tls.LoadX509KeyPair(certFile, keyFile)
	leaf, _ := x509.ParseCertificate(pair.Certificate[0])
	found := false
	for _, ip := range leaf.IPAddresses {
		found = found || ip.Equal(net.ParseIP("192.0.2.1"))
	}
	if !found {
		t.Fatalf("IP SAN missing: %v", leaf.IPAddresses)
	}
}
