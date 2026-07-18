// Package server owns wattlined's network listeners and local TLS identity.
package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Certificate struct {
	CertFile string
	KeyFile  string
	SHA256   string
}

func EnsureCertificate(certFile, keyFile string, names []string) (Certificate, error) {
	certInfo, certErr := os.Stat(certFile)
	keyInfo, keyErr := os.Stat(keyFile)
	switch {
	case errors.Is(certErr, os.ErrNotExist) && errors.Is(keyErr, os.ErrNotExist):
		return writeNewCertificate(certFile, keyFile, names, false)
	case certErr != nil:
		return Certificate{}, fmt.Errorf("stat TLS certificate: %w", certErr)
	case keyErr != nil:
		return Certificate{}, fmt.Errorf("stat TLS private key: %w", keyErr)
	case !certInfo.Mode().IsRegular() || !keyInfo.Mode().IsRegular():
		return Certificate{}, errors.New("TLS certificate and key must be regular files")
	}
	return loadCertificate(certFile, keyFile, names)
}

func RotateCertificate(certFile, keyFile string, names []string) (Certificate, error) {
	_, certErr := os.Stat(certFile)
	_, keyErr := os.Stat(keyFile)
	if errors.Is(certErr, os.ErrNotExist) && errors.Is(keyErr, os.ErrNotExist) {
		return writeNewCertificate(certFile, keyFile, names, false)
	}
	if certErr != nil || keyErr != nil {
		return Certificate{}, errors.New("refuse to rotate one-sided TLS pair")
	}
	return writeNewCertificate(certFile, keyFile, names, true)
}

func loadCertificate(certFile, keyFile string, names []string) (Certificate, error) {
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return Certificate{}, fmt.Errorf("load TLS certificate pair: %w", err)
	}
	if len(pair.Certificate) == 0 {
		return Certificate{}, errors.New("TLS certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return Certificate{}, fmt.Errorf("parse TLS certificate: %w", err)
	}
	key, ok := pair.PrivateKey.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return Certificate{}, errors.New("TLS private key must be ECDSA P-256")
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return Certificate{}, errors.New("TLS certificate is not currently valid")
	}
	if leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 || !hasServerAuth(leaf.ExtKeyUsage) {
		return Certificate{}, errors.New("TLS certificate is not valid for server authentication")
	}
	for _, name := range certificateNames(names) {
		if err := leaf.VerifyHostname(name); err != nil {
			return Certificate{}, fmt.Errorf("TLS certificate does not cover %q: %w", name, err)
		}
	}
	if err := os.Chmod(keyFile, 0o600); err != nil {
		return Certificate{}, fmt.Errorf("secure TLS private key: %w", err)
	}
	sum := sha256.Sum256(leaf.Raw)
	return Certificate{CertFile: certFile, KeyFile: keyFile, SHA256: hex.EncodeToString(sum[:])}, nil
}

func hasServerAuth(usages []x509.ExtKeyUsage) bool {
	for _, usage := range usages {
		if usage == x509.ExtKeyUsageServerAuth || usage == x509.ExtKeyUsageAny {
			return true
		}
	}
	return false
}

func certificateNames(names []string) []string {
	set := map[string]struct{}{"localhost": {}, "127.0.0.1": {}, "::1": {}}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" {
			set[name] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for name := range set {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func generateCertificate(names []string) (certPEM, keyPEM []byte, fingerprint string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, "", fmt.Errorf("generate TLS key: %w", err)
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, nil, "", fmt.Errorf("generate TLS serial: %w", err)
	}
	allNames := certificateNames(names)
	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "wattline"},
		NotBefore:             time.Now().Add(-time.Hour).UTC(),
		NotAfter:              time.Now().AddDate(10, 0, 0).UTC(),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, name := range allNames {
		if ip := net.ParseIP(name); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, name)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, "", fmt.Errorf("create TLS certificate: %w", err)
	}
	encodedKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, "", fmt.Errorf("encode TLS key: %w", err)
	}
	sum := sha256.Sum256(der)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey}), hex.EncodeToString(sum[:]), nil
}

func writeNewCertificate(certFile, keyFile string, names []string, replacing bool) (Certificate, error) {
	if certFile == "" || keyFile == "" || certFile == keyFile {
		return Certificate{}, errors.New("TLS certificate and key paths must be distinct")
	}
	if replacing {
		if _, err := loadCertificate(certFile, keyFile, nil); err != nil {
			return Certificate{}, fmt.Errorf("refuse to rotate invalid TLS pair: %w", err)
		}
	}
	certPEM, keyPEM, fingerprint, err := generateCertificate(names)
	if err != nil {
		return Certificate{}, err
	}
	if err := os.MkdirAll(filepath.Dir(certFile), 0o755); err != nil {
		return Certificate{}, err
	}
	if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
		return Certificate{}, err
	}
	certTemp, err := stageFile(certFile, certPEM, 0o644)
	if err != nil {
		return Certificate{}, err
	}
	defer os.Remove(certTemp)
	keyTemp, err := stageFile(keyFile, keyPEM, 0o600)
	if err != nil {
		return Certificate{}, err
	}
	defer os.Remove(keyTemp)
	if err := replacePair(certFile, keyFile, certTemp, keyTemp, replacing); err != nil {
		return Certificate{}, err
	}
	return Certificate{CertFile: certFile, KeyFile: keyFile, SHA256: fingerprint}, nil
}

func stageFile(target string, contents []byte, mode os.FileMode) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(name)
		}
	}()
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return "", err
	}
	if _, err := f.Write(contents); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	ok = true
	return name, nil
}

func replacePair(certFile, keyFile, certTemp, keyTemp string, replacing bool) error {
	certBackup, keyBackup := certFile+".rotate-backup", keyFile+".rotate-backup"
	cleanup := func() { _ = os.Remove(certBackup); _ = os.Remove(keyBackup) }
	cleanup()
	if replacing {
		if err := os.Rename(certFile, certBackup); err != nil {
			return err
		}
		if err := os.Rename(keyFile, keyBackup); err != nil {
			_ = os.Rename(certBackup, certFile)
			return err
		}
	}
	rollback := func() {
		_ = os.Remove(certFile)
		_ = os.Remove(keyFile)
		if replacing {
			_ = os.Rename(certBackup, certFile)
			_ = os.Rename(keyBackup, keyFile)
		}
	}
	if err := os.Rename(certTemp, certFile); err != nil {
		rollback()
		return err
	}
	if err := os.Rename(keyTemp, keyFile); err != nil {
		rollback()
		return err
	}
	cleanup()
	return nil
}
