// Package certs serves a TLS certificate from PEM files and reloads it when the files
// change or on request (SIGHUP), so a renewed certificate needs no restart.
package certs

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// Loader holds the current certificate of a cert/key file pair.
type Loader struct {
	certFile, keyFile string
	log               *slog.Logger

	mu   sync.RWMutex
	cert *tls.Certificate
	// stamp identifies the files' state at the last load attempt, so a broken pair is
	// reported once rather than on every check; it is tried again when a file changes or
	// on SIGHUP.
	stamp stamp
}

type stamp struct {
	certMod, keyMod   time.Time
	certSize, keySize int64
}

// Load reads the pair; it fails if the files are missing or don't hold a matching
// certificate and key, so a bad configuration stops the server at start.
func Load(certFile, keyFile string, log *slog.Logger) (*Loader, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	l := &Loader{certFile: certFile, keyFile: keyFile, log: log}
	st, err := l.stat()
	if err != nil {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS certificate: %w", err)
	}
	l.cert, l.stamp = &cert, st
	return l, nil
}

// GetCertificate is for tls.Config.GetCertificate.
func (l *Loader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.cert, nil
}

// Reload reads the files again. On failure the previous certificate stays in use.
func (l *Loader) Reload() error {
	st, err := l.stat()
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.stamp = st
	l.mu.Unlock()
	cert, err := tls.LoadX509KeyPair(l.certFile, l.keyFile)
	if err != nil {
		return fmt.Errorf("load TLS certificate: %w", err)
	}
	l.mu.Lock()
	l.cert = &cert
	l.mu.Unlock()
	return nil
}

// Watch reloads on every value from hup, and on a tick when either file changed since
// the last load, until ctx ends. Failures are logged; the old certificate stays.
func (l *Loader) Watch(ctx context.Context, tick <-chan time.Time, hup <-chan os.Signal) {
	lastStatErr := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-hup:
			l.reload("signal")
		case <-tick:
			st, err := l.stat()
			if err != nil {
				// Mid-renewal a file may be missing for a moment; the next tick retries.
				// Report each distinct failure once, so a bad mount doesn't go unnoticed.
				if err.Error() != lastStatErr {
					l.log.Warn("TLS certificate files unreadable, keeping the current certificate", "err", err)
					lastStatErr = err.Error()
				}
				continue
			}
			lastStatErr = ""
			l.mu.RLock()
			changed := st != l.stamp
			l.mu.RUnlock()
			if changed {
				l.reload("file change")
			}
		}
	}
}

func (l *Loader) reload(reason string) {
	if err := l.Reload(); err != nil {
		l.log.Error("TLS certificate reload failed, keeping the previous one", "reason", reason, "err", err)
		return
	}
	l.log.Info("TLS certificate reloaded", "reason", reason, "cert", l.certFile)
}

func (l *Loader) stat() (stamp, error) {
	c, err := os.Stat(l.certFile)
	if err != nil {
		return stamp{}, fmt.Errorf("TLS certificate: %w", err)
	}
	k, err := os.Stat(l.keyFile)
	if err != nil {
		return stamp{}, fmt.Errorf("TLS key: %w", err)
	}
	return stamp{certMod: c.ModTime(), keyMod: k.ModTime(), certSize: c.Size(), keySize: k.Size()}, nil
}
