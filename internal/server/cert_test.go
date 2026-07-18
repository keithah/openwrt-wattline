package server

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if leaf.SerialNumber.Sign() <= 0 {
		t.Fatal("certificate serial is not positive")
	}
	if err := leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature); err != nil || leaf.Issuer.String() != leaf.Subject.String() {
		t.Fatalf("certificate is not self-signed: %v", err)
	}
	if leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 || !hasServerAuth(leaf.ExtKeyUsage) {
		t.Fatalf("server usages missing: %v %v", leaf.KeyUsage, leaf.ExtKeyUsage)
	}
	if !leaf.NotBefore.Before(time.Now()) || !leaf.NotAfter.After(time.Now().AddDate(9, 0, 0)) {
		t.Fatalf("unexpected validity: %s..%s", leaf.NotBefore, leaf.NotAfter)
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

func TestCertificateRejectsSymlinksUnsafeParentsAndAliases(t *testing.T) {
	t.Run("symlink file", func(t *testing.T) {
		dir := t.TempDir()
		realDir := filepath.Join(dir, "real")
		os.Mkdir(realDir, 0o700)
		cert, key := filepath.Join(realDir, "cert"), filepath.Join(realDir, "key")
		if _, err := EnsureCertificate(cert, key, nil); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "cert-link")
		if err := os.Symlink(cert, link); err != nil {
			t.Fatal(err)
		}
		if _, err := EnsureCertificate(link, key, nil); err == nil {
			t.Fatal("symlink certificate accepted")
		}
	})
	t.Run("symlink component", func(t *testing.T) {
		dir := t.TempDir()
		realDir := filepath.Join(dir, "real")
		os.Mkdir(realDir, 0o700)
		linkDir := filepath.Join(dir, "link")
		if err := os.Symlink(realDir, linkDir); err != nil {
			t.Fatal(err)
		}
		if _, err := EnsureCertificate(filepath.Join(linkDir, "cert"), filepath.Join(linkDir, "key"), nil); err == nil {
			t.Fatal("symlink path component accepted")
		}
	})
	t.Run("unsafe immediate parent", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "unsafe")
		if err := os.Mkdir(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		if _, err := EnsureCertificate(filepath.Join(dir, "cert"), filepath.Join(dir, "key"), nil); err == nil {
			t.Fatal("unsafe parent accepted")
		}
	})
	t.Run("transaction alias", func(t *testing.T) {
		dir := t.TempDir()
		cert := filepath.Join(dir, "cert")
		if _, err := EnsureCertificate(cert, transactionPath(cert), nil); err == nil {
			t.Fatal("transaction alias accepted")
		}
	})
}

func TestCertificateRotationRecoversEveryInterruptedRename(t *testing.T) {
	steps := []string{"backup-cert", "backup-key", "install-cert", "install-key"}
	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			dir := t.TempDir()
			certFile, keyFile := filepath.Join(dir, "cert"), filepath.Join(dir, "key")
			old, err := EnsureCertificate(certFile, keyFile, nil)
			if err != nil {
				t.Fatal(err)
			}
			crash := errors.New("simulated interruption")
			_, err = rotateCertificateWithHook(certFile, keyFile, nil, func(got string) error {
				if got == step {
					return crash
				}
				return nil
			})
			if !errors.Is(err, crash) {
				t.Fatalf("rotation error = %v", err)
			}
			recovered, err := EnsureCertificate(certFile, keyFile, nil)
			if err != nil {
				t.Fatalf("recovery: %v", err)
			}
			if recovered.SHA256 == "" {
				t.Fatal("recovery has no fingerprint")
			}
			if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
				t.Fatalf("recovered pair mismatched: %v", err)
			}
			if _, err := os.Lstat(transactionPath(certFile)); !os.IsNotExist(err) {
				t.Fatalf("journal remains: %v", err)
			}
			if recovered.SHA256 != old.SHA256 {
				t.Fatalf("recovery chose neither the intact old pair: old=%s recovered=%s", old.SHA256, recovered.SHA256)
			}
		})
	}
}

func TestCertificateFreshCreationRecoversInterruptedInstall(t *testing.T) {
	for _, step := range []string{"install-cert", "install-key"} {
		t.Run(step, func(t *testing.T) {
			dir := t.TempDir()
			certFile, keyFile := filepath.Join(dir, "cert"), filepath.Join(dir, "key")
			crash := errors.New("crash")
			if _, err := rotateCertificateWithHook(certFile, keyFile, nil, func(got string) error {
				if got == step {
					return crash
				}
				return nil
			}); !errors.Is(err, crash) {
				t.Fatalf("error=%v", err)
			}
			if _, err := EnsureCertificate(certFile, keyFile, nil); err != nil {
				t.Fatalf("recover fresh pair: %v", err)
			}
			if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
				t.Fatalf("recovered mismatch: %v", err)
			}
		})
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
