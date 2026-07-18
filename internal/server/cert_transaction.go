package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type certificateTransaction struct {
	CertFile   string `json:"cert_file"`
	KeyFile    string `json:"key_file"`
	CertTemp   string `json:"cert_temp"`
	KeyTemp    string `json:"key_temp"`
	CertBackup string `json:"cert_backup,omitempty"`
	KeyBackup  string `json:"key_backup,omitempty"`
}

func transactionPath(certFile string) string { return certFile + ".wattline-tls-transaction" }

func rotateCertificateWithHook(certFile, keyFile string, names []string, hook func(string) error) (Certificate, error) {
	return rotateCertificateWithHooks(certFile, keyFile, names, certificateOperationHooks{beforeRename: hook})
}

func rotateCertificateWithHooks(certFile, keyFile string, names []string, hooks certificateOperationHooks) (Certificate, error) {
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
	installed, err := writeTransaction(tx, hooks.journalSync)
	if err != nil {
		if installed {
			return Certificate{}, err
		}
		return Certificate{}, cleanupOnPrepareError(err, certTemp, keyTemp, tx.CertBackup, tx.KeyBackup)
	}
	renameStep := func(step, old, new string) error {
		if hooks.beforeRename != nil {
			if err := hooks.beforeRename(step); err != nil {
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

func writeTransaction(tx certificateTransaction, journalSync func(string) error) (bool, error) {
	raw, err := json.Marshal(tx)
	if err != nil {
		return false, err
	}
	path := transactionPath(tx.CertFile)
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return false, errors.New("TLS transaction journal already exists")
		}
		return false, err
	}
	temp, err := stageFile(path, raw, 0o600)
	if err != nil {
		return false, err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			err = errors.New("TLS transaction journal appeared concurrently")
		}
		return false, errors.Join(err, removeIfExists(temp))
	}
	if err := os.Rename(temp, path); err != nil {
		return false, errors.Join(err, removeIfExists(temp))
	}
	if journalSync == nil {
		journalSync = syncDirectory
	}
	if err := journalSync(filepath.Dir(path)); err != nil {
		return true, err
	}
	return true, nil
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
