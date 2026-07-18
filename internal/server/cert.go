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
	"sort"
	"strings"
	"sync"
	"time"
)

type Certificate struct{ CertFile, KeyFile, SHA256 string }

type certificateOperationHooks struct {
	afterInitialValidation func() error
	beforeRename           func(string) error
	journalSync            func(string) error
}

var certificateOperationMu sync.Mutex

func EnsureCertificate(certFile, keyFile string, names []string) (Certificate, error) {
	return ensureCertificateWithHooks(certFile, keyFile, names, certificateOperationHooks{})
}

func ensureCertificateWithHooks(certFile, keyFile string, names []string, hooks certificateOperationHooks) (Certificate, error) {
	certificateOperationMu.Lock()
	defer certificateOperationMu.Unlock()
	if err := prepareTLSPaths(certFile, keyFile); err != nil {
		return Certificate{}, err
	}
	if hooks.afterInitialValidation != nil {
		if err := hooks.afterInitialValidation(); err != nil {
			return Certificate{}, err
		}
	}
	if err := prepareTLSPaths(certFile, keyFile); err != nil {
		return Certificate{}, err
	}
	locks, err := acquireCertificateLocks(certFile, keyFile)
	if err != nil {
		return Certificate{}, err
	}
	if err := prepareTLSPaths(certFile, keyFile); err != nil {
		return Certificate{}, errors.Join(err, locks.release())
	}
	result, operationErr := ensureCertificateLocked(certFile, keyFile, names, hooks)
	return result, errors.Join(operationErr, locks.release())
}

func ensureCertificateLocked(certFile, keyFile string, names []string, hooks certificateOperationHooks) (Certificate, error) {
	if err := recoverCertificateTransaction(certFile, keyFile); err != nil {
		return Certificate{}, err
	}
	certExists, err := regularFileState(certFile)
	if err != nil {
		return Certificate{}, err
	}
	keyExists, err := regularFileState(keyFile)
	if err != nil {
		return Certificate{}, err
	}
	switch {
	case !certExists && !keyExists:
		return rotateCertificateWithHooks(certFile, keyFile, names, hooks)
	case certExists != keyExists:
		return Certificate{}, errors.New("TLS certificate pair is one-sided")
	default:
		return loadCertificate(certFile, keyFile, names)
	}
}

func RotateCertificate(certFile, keyFile string, names []string) (Certificate, error) {
	certificateOperationMu.Lock()
	defer certificateOperationMu.Unlock()
	if err := prepareTLSPaths(certFile, keyFile); err != nil {
		return Certificate{}, err
	}
	locks, err := acquireCertificateLocks(certFile, keyFile)
	if err != nil {
		return Certificate{}, err
	}
	if err := prepareTLSPaths(certFile, keyFile); err != nil {
		return Certificate{}, errors.Join(err, locks.release())
	}
	result, operationErr := rotateCertificateLocked(certFile, keyFile, names)
	return result, errors.Join(operationErr, locks.release())
}

func rotateCertificateLocked(certFile, keyFile string, names []string) (Certificate, error) {
	if err := recoverCertificateTransaction(certFile, keyFile); err != nil {
		return Certificate{}, err
	}
	certExists, err := regularFileState(certFile)
	if err != nil {
		return Certificate{}, err
	}
	keyExists, err := regularFileState(keyFile)
	if err != nil {
		return Certificate{}, err
	}
	if certExists != keyExists {
		return Certificate{}, errors.New("refuse to rotate one-sided TLS pair")
	}
	if certExists {
		if _, err := loadCertificate(certFile, keyFile, nil); err != nil {
			return Certificate{}, fmt.Errorf("refuse to rotate invalid TLS pair: %w", err)
		}
	}
	return rotateCertificateWithHooks(certFile, keyFile, names, certificateOperationHooks{})
}

func regularFileState(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, fmt.Errorf("TLS path %q is not a regular file", path)
	}
	return true, nil
}

func loadCertificate(certFile, keyFile string, names []string) (Certificate, error) {
	keyInfo, err := os.Lstat(keyFile)
	if err != nil {
		return Certificate{}, err
	}
	if keyInfo.Mode().Perm()&0o077 != 0 {
		return Certificate{}, errors.New("TLS private key permissions are too broad")
	}
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
	sum := sha256.Sum256(leaf.Raw)
	return Certificate{certFile, keyFile, hex.EncodeToString(sum[:])}, nil
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
		if name = strings.TrimSpace(name); name != "" {
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
	var serial *big.Int
	for serial == nil || serial.Sign() == 0 {
		serial, err = rand.Int(rand.Reader, serialLimit)
		if err != nil {
			return nil, nil, "", fmt.Errorf("generate TLS serial: %w", err)
		}
	}
	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "wattline"},
		NotBefore:             time.Now().Add(-time.Hour).UTC(),
		NotAfter:              time.Now().AddDate(10, 0, 0).UTC(),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, name := range certificateNames(names) {
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
