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
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type Certificate struct{ CertFile, KeyFile, SHA256 string }

type certificateTransaction struct {
	CertFile   string `json:"cert_file"`
	KeyFile    string `json:"key_file"`
	CertTemp   string `json:"cert_temp"`
	KeyTemp    string `json:"key_temp"`
	CertBackup string `json:"cert_backup,omitempty"`
	KeyBackup  string `json:"key_backup,omitempty"`
}

func transactionPath(certFile string) string { return certFile + ".wattline-tls-transaction" }

func EnsureCertificate(certFile, keyFile string, names []string) (Certificate, error) {
	if err := prepareTLSPaths(certFile, keyFile); err != nil {
		return Certificate{}, err
	}
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
		return rotateCertificateWithHook(certFile, keyFile, names, nil)
	case certExists != keyExists:
		return Certificate{}, errors.New("TLS certificate pair is one-sided")
	default:
		return loadCertificate(certFile, keyFile, names)
	}
}

func RotateCertificate(certFile, keyFile string, names []string) (Certificate, error) {
	if err := prepareTLSPaths(certFile, keyFile); err != nil {
		return Certificate{}, err
	}
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
	return rotateCertificateWithHook(certFile, keyFile, names, nil)
}

func prepareTLSPaths(certFile, keyFile string) error {
	if certFile == "" || keyFile == "" || !filepath.IsAbs(certFile) || !filepath.IsAbs(keyFile) || filepath.Clean(certFile) != certFile || filepath.Clean(keyFile) != keyFile {
		return errors.New("TLS certificate and key paths must be clean absolute paths")
	}
	journal := transactionPath(certFile)
	paths := []string{certFile, keyFile, journal}
	for i := range paths {
		for j := i + 1; j < len(paths); j++ {
			if paths[i] == paths[j] {
				return errors.New("TLS certificate transaction paths alias")
			}
		}
	}
	for _, parent := range []string{filepath.Dir(certFile), filepath.Dir(keyFile)} {
		if err := secureParent(parent); err != nil {
			return err
		}
	}
	for _, path := range paths {
		if err := rejectSymlinkComponents(path); err != nil {
			return err
		}
	}
	certInfo, certErr := os.Lstat(certFile)
	keyInfo, keyErr := os.Lstat(keyFile)
	if certErr == nil && keyErr == nil && os.SameFile(certInfo, keyInfo) {
		return errors.New("TLS certificate and key files alias")
	}
	if journalInfo, journalErr := os.Lstat(journal); journalErr == nil {
		if (certErr == nil && os.SameFile(certInfo, journalInfo)) || (keyErr == nil && os.SameFile(keyInfo, journalInfo)) {
			return errors.New("TLS transaction journal aliases a target")
		}
	}
	return nil
}

func secureParent(parent string) error {
	if err := rejectSymlinkComponents(parent); err != nil {
		return err
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create TLS directory: %w", err)
	}
	if err := rejectSymlinkComponents(parent); err != nil {
		return err
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("TLS parent %q is not a directory", parent)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("TLS parent %q is not owned by effective uid", parent)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("TLS parent %q is group/other writable", parent)
	}
	return syncDirectory(parent)
}

func rejectSymlinkComponents(path string) error {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	rest := strings.TrimPrefix(clean, volume)
	current := volume + string(os.PathSeparator)
	for _, component := range strings.Split(strings.TrimPrefix(rest, string(os.PathSeparator)), string(os.PathSeparator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("TLS path component %q is a symlink", current)
		}
	}
	return nil
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

func rotateCertificateWithHook(certFile, keyFile string, names []string, hook func(string) error) (Certificate, error) {
	certPEM, keyPEM, fingerprint, err := generateCertificate(names)
	if err != nil {
		return Certificate{}, err
	}
	certTemp, err := stageFile(certFile, certPEM, 0o644)
	if err != nil {
		return Certificate{}, err
	}
	keyTemp, err := stageFile(keyFile, keyPEM, 0o600)
	if err != nil {
		return Certificate{}, errors.Join(err, removeAndSync(certTemp))
	}
	certExists, _ := regularFileState(certFile)
	keyExists, _ := regularFileState(keyFile)
	tx := certificateTransaction{CertFile: certFile, KeyFile: keyFile, CertTemp: certTemp, KeyTemp: keyTemp}
	if certExists {
		if tx.CertBackup, err = reserveSibling(certFile, "backup"); err != nil {
			return Certificate{}, cleanupOnPrepareError(err, certTemp, keyTemp)
		}
	}
	if keyExists {
		if tx.KeyBackup, err = reserveSibling(keyFile, "backup"); err != nil {
			return Certificate{}, cleanupOnPrepareError(err, certTemp, keyTemp, tx.CertBackup)
		}
	}
	if err := writeTransaction(tx); err != nil {
		return Certificate{}, cleanupOnPrepareError(err, certTemp, keyTemp, tx.CertBackup, tx.KeyBackup)
	}
	renameStep := func(step, old, new string) error {
		if hook != nil {
			if err := hook(step); err != nil {
				return err
			}
		}
		if err := os.Rename(old, new); err != nil {
			recoveryErr := recoverCertificateTransaction(certFile, keyFile)
			return errors.Join(fmt.Errorf("%s: %w", step, err), recoveryErr)
		}
		if err := syncRelevantDirs(old, new); err != nil {
			recoveryErr := recoverCertificateTransaction(certFile, keyFile)
			return errors.Join(fmt.Errorf("sync %s: %w", step, err), recoveryErr)
		}
		return nil
	}
	if tx.CertBackup != "" {
		if err := renameStep("backup-cert", certFile, tx.CertBackup); err != nil {
			return Certificate{}, err
		}
	}
	if tx.KeyBackup != "" {
		if err := renameStep("backup-key", keyFile, tx.KeyBackup); err != nil {
			return Certificate{}, err
		}
	}
	if err := renameStep("install-cert", certTemp, certFile); err != nil {
		return Certificate{}, err
	}
	if err := renameStep("install-key", keyTemp, keyFile); err != nil {
		return Certificate{}, err
	}
	if _, err := loadCertificate(certFile, keyFile, names); err != nil {
		recoveryErr := recoverCertificateTransaction(certFile, keyFile)
		return Certificate{}, errors.Join(err, recoveryErr)
	}
	if err := finishTransaction(tx); err != nil {
		return Certificate{}, err
	}
	return Certificate{certFile, keyFile, fingerprint}, nil
}

func reserveSibling(target, kind string) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+"."+kind+"-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		return "", errors.Join(err, removeIfExists(name))
	}
	if err := os.Remove(name); err != nil {
		return "", err
	}
	if err := syncDirectory(filepath.Dir(target)); err != nil {
		return "", err
	}
	return name, nil
}

func stageFile(target string, contents []byte, mode os.FileMode) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	fail := func(cause error) (string, error) {
		return "", errors.Join(cause, f.Close(), removeIfExists(name))
	}
	if err := f.Chmod(mode); err != nil {
		return fail(err)
	}
	if _, err := f.Write(contents); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		return "", errors.Join(err, removeIfExists(name))
	}
	if err := syncDirectory(filepath.Dir(target)); err != nil {
		return "", errors.Join(err, removeIfExists(name))
	}
	return name, nil
}

func writeTransaction(tx certificateTransaction) error {
	raw, err := json.Marshal(tx)
	if err != nil {
		return err
	}
	path := transactionPath(tx.CertFile)
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return errors.New("TLS transaction journal already exists")
		}
		return err
	}
	temp, err := stageFile(path, raw, 0o600)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			err = errors.New("TLS transaction journal appeared concurrently")
		}
		return errors.Join(err, removeIfExists(temp))
	}
	if err := os.Rename(temp, path); err != nil {
		return errors.Join(err, removeIfExists(temp))
	}
	return syncDirectory(filepath.Dir(path))
}

func recoverCertificateTransaction(certFile, keyFile string) error {
	path := transactionPath(certFile)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var tx certificateTransaction
	if err := json.Unmarshal(raw, &tx); err != nil {
		return fmt.Errorf("decode TLS transaction journal: %w", err)
	}
	if tx.CertFile != certFile || tx.KeyFile != keyFile {
		return errors.New("TLS transaction journal targets do not match configured paths")
	}
	if err := validateTransactionPaths(tx); err != nil {
		return err
	}
	all := []string{tx.CertFile, tx.KeyFile, tx.CertTemp, tx.KeyTemp, tx.CertBackup, tx.KeyBackup, path}
	seen := map[string]struct{}{}
	var existing []os.FileInfo
	for _, p := range all {
		if p == "" {
			continue
		}
		if filepath.Clean(p) != p {
			return errors.New("TLS transaction contains unclean path")
		}
		if _, ok := seen[p]; ok {
			return errors.New("TLS transaction paths alias")
		}
		seen[p] = struct{}{}
		if info, err := os.Lstat(p); err == nil {
			for _, prior := range existing {
				if os.SameFile(info, prior) {
					return errors.New("TLS transaction filesystem objects alias")
				}
			}
			existing = append(existing, info)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	type candidate struct {
		cert, key               string
		installCert, installKey bool
	}
	candidates := []candidate{
		{tx.CertFile, tx.KeyFile, false, false},
		{tx.CertBackup, tx.KeyBackup, true, true},
		{tx.CertBackup, tx.KeyFile, true, false},
		{tx.CertFile, tx.KeyBackup, false, true},
		{tx.CertTemp, tx.KeyTemp, true, true},
		{tx.CertFile, tx.KeyTemp, false, true},
		{tx.CertTemp, tx.KeyFile, true, false},
	}
	var chosen *candidate
	for i := range candidates {
		c := &candidates[i]
		if c.cert == "" || c.key == "" {
			continue
		}
		if pairFilesUsable(c.cert, c.key) {
			chosen = c
			break
		}
	}
	if chosen == nil {
		return errors.New("TLS transaction cannot recover a matching certificate pair")
	}
	if chosen.installCert {
		if err := replaceRecovered(chosen.cert, certFile); err != nil {
			return err
		}
	}
	if chosen.installKey {
		if err := replaceRecovered(chosen.key, keyFile); err != nil {
			return err
		}
	}
	if !pairFilesUsable(certFile, keyFile) {
		return errors.New("TLS transaction recovery produced an unusable pair")
	}
	return finishTransaction(tx)
}

func validateTransactionPaths(tx certificateTransaction) error {
	tests := []struct {
		path, target, kind string
		optional           bool
	}{
		{tx.CertTemp, tx.CertFile, "tmp", false},
		{tx.KeyTemp, tx.KeyFile, "tmp", false},
		{tx.CertBackup, tx.CertFile, "backup", true},
		{tx.KeyBackup, tx.KeyFile, "backup", true},
	}
	for _, test := range tests {
		if test.path == "" && test.optional {
			continue
		}
		if test.path == "" {
			return errors.New("TLS transaction is missing staged path")
		}
		prefix := "." + filepath.Base(test.target) + "." + test.kind + "-"
		if filepath.Dir(test.path) != filepath.Dir(test.target) || !strings.HasPrefix(filepath.Base(test.path), prefix) {
			return errors.New("TLS transaction auxiliary path is outside its secure target directory")
		}
		if err := rejectSymlinkComponents(test.path); err != nil {
			return err
		}
	}
	return nil
}

func pairFilesUsable(certFile, keyFile string) bool {
	_, err := loadCertificate(certFile, keyFile, nil)
	return err == nil
}

func replaceRecovered(source, target string) error {
	if source == target {
		return nil
	}
	if err := removeIfExists(target); err != nil {
		return err
	}
	if err := os.Rename(source, target); err != nil {
		return err
	}
	return syncRelevantDirs(source, target)
}

func finishTransaction(tx certificateTransaction) error {
	for _, path := range []string{tx.CertTemp, tx.KeyTemp, tx.CertBackup, tx.KeyBackup} {
		if err := removeIfExists(path); err != nil {
			return err
		}
	}
	journal := transactionPath(tx.CertFile)
	if err := removeIfExists(journal); err != nil {
		return err
	}
	return syncRelevantDirs(tx.CertFile, tx.KeyFile, journal)
}

func cleanupOnPrepareError(cause error, paths ...string) error {
	var errs []error
	errs = append(errs, cause)
	for _, p := range paths {
		if p != "" {
			if err := removeIfExists(p); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func removeIfExists(path string) error {
	if path == "" {
		return nil
	}
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
func removeAndSync(path string) error { return removeIfExists(path) }

func syncRelevantDirs(paths ...string) error {
	seen := map[string]struct{}{}
	for _, path := range paths {
		if path == "" {
			continue
		}
		dir := filepath.Dir(path)
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		if err := syncDirectory(dir); err != nil {
			return err
		}
	}
	return nil
}
func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	return errors.Join(syncErr, closeErr)
}
