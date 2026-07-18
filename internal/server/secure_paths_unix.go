//go:build unix

package server

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

func lockPath(target string) string { return target + ".wattline-tls-lock" }

func prepareTLSPaths(certFile, keyFile string) error {
	if certFile == "" || keyFile == "" || !filepath.IsAbs(certFile) || !filepath.IsAbs(keyFile) || filepath.Clean(certFile) != certFile || filepath.Clean(keyFile) != keyFile {
		return errors.New("TLS certificate and key paths must be clean absolute paths")
	}
	journal := transactionPath(certFile)
	paths := []string{certFile, keyFile, journal, lockPath(certFile), lockPath(keyFile)}
	for i := range paths {
		for j := i + 1; j < len(paths); j++ {
			if paths[i] == paths[j] {
				return errors.New("TLS certificate transaction paths alias")
			}
		}
	}
	for _, parent := range []string{filepath.Dir(certFile), filepath.Dir(keyFile)} {
		if err := ensureTrustedParent(parent); err != nil {
			return err
		}
	}
	for _, path := range paths {
		if err := rejectSymlinkComponents(path); err != nil {
			return err
		}
	}
	return rejectExistingAliases(paths)
}

func ensureTrustedParent(parent string) error {
	components := absoluteComponents(parent)
	infos := make([]os.FileInfo, 0, len(components))
	for _, path := range components {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("create TLS directory: %w", err)
			}
			info, err = os.Lstat(path)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("TLS ancestor %q is not a real directory", path)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (int(stat.Uid) != 0 && int(stat.Uid) != os.Geteuid()) {
			return fmt.Errorf("TLS ancestor %q has untrusted owner", path)
		}
		infos = append(infos, info)
	}
	for i, info := range infos {
		if info.Mode().Perm()&0o022 == 0 {
			continue
		}
		if info.Mode()&os.ModeSticky == 0 || i+1 >= len(infos) || infos[i+1].Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("TLS ancestor %q is unsafely writable", components[i])
		}
	}
	return syncDirectory(parent)
}

func absoluteComponents(path string) []string {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	rest := strings.TrimPrefix(clean, volume)
	current := volume + string(os.PathSeparator)
	result := []string{current}
	for _, component := range strings.Split(strings.TrimPrefix(rest, string(os.PathSeparator)), string(os.PathSeparator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		result = append(result, current)
	}
	return result
}

func rejectExistingAliases(paths []string) error {
	var existing []os.FileInfo
	for _, path := range paths {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		for _, prior := range existing {
			if os.SameFile(info, prior) {
				return errors.New("TLS operation paths alias")
			}
		}
		existing = append(existing, info)
	}
	return nil
}

type certificateLocks struct{ files []*os.File }

func acquireCertificateLocks(certFile, keyFile string) (*certificateLocks, error) {
	paths := []string{lockPath(certFile), lockPath(keyFile)}
	sort.Strings(paths)
	locks := &certificateLocks{}
	for _, path := range paths {
		fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("open TLS lock %q: %w", path, err), locks.release())
		}
		file := os.NewFile(uintptr(fd), path)
		if file == nil {
			_ = syscall.Close(fd)
			return nil, errors.Join(errors.New("wrap TLS lock file"), locks.release())
		}
		var stat syscall.Stat_t
		if err := syscall.Fstat(fd, &stat); err != nil {
			closeErr := file.Close()
			return nil, errors.Join(err, closeErr, locks.release())
		}
		if stat.Mode&syscall.S_IFMT != syscall.S_IFREG || (int(stat.Uid) != 0 && int(stat.Uid) != os.Geteuid()) || os.FileMode(stat.Mode).Perm()&0o077 != 0 {
			closeErr := file.Close()
			return nil, errors.Join(fmt.Errorf("TLS lock %q is not a secure regular file", path), closeErr, locks.release())
		}
		for {
			err = syscall.Flock(fd, syscall.LOCK_EX)
			if !errors.Is(err, syscall.EINTR) {
				break
			}
		}
		if err != nil {
			closeErr := file.Close()
			return nil, errors.Join(err, closeErr, locks.release())
		}
		locks.files = append(locks.files, file)
	}
	return locks, nil
}

func (l *certificateLocks) release() error {
	if l == nil {
		return nil
	}
	var errs []error
	for i := len(l.files) - 1; i >= 0; i-- {
		file := l.files[i]
		if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
			errs = append(errs, err)
		}
		if err := file.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	l.files = nil
	return errors.Join(errs...)
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
