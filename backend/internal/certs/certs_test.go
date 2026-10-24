package certs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// writePair writes a self-signed certificate with serial and its key to dir, with the
// given modification time.
func writePair(t *testing.T, dir string, serial int64, mod time.Time) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "nudge.test"},
		DNSNames:     []string{"nudge.test"},
		NotBefore:    time.Unix(1_700_000_000, 0),
		NotAfter:     time.Unix(1_700_000_000, 0).Add(365 * 24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	writeFile(t, certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), mod)
	writeFile(t, keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), mod)
	return certFile, keyFile
}

func writeFile(t *testing.T, path string, b []byte, mod time.Time) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func serial(t *testing.T, l *Loader) int64 {
	t.Helper()
	c, err := l.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf.SerialNumber.Int64()
}

func TestLoadErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	certFile, keyFile := writePair(t, dir, 1, time.Unix(1_700_000_000, 0))
	other := t.TempDir()
	_, otherKey := writePair(t, other, 2, time.Unix(1_700_000_000, 0))
	garbage := filepath.Join(dir, "garbage.pem")
	writeFile(t, garbage, []byte("not pem"), time.Unix(1_700_000_000, 0))
	tests := []struct {
		name      string
		cert, key string
	}{
		{name: "missing cert", cert: filepath.Join(dir, "nope.pem"), key: keyFile},
		{name: "missing key", cert: certFile, key: filepath.Join(dir, "nope.pem")},
		{name: "key of another cert", cert: certFile, key: otherKey},
		{name: "not PEM", cert: garbage, key: keyFile},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := Load(tt.cert, tt.key, nil); err == nil {
				t.Error("Load succeeded")
			}
		})
	}
}

// watch runs l.Watch until the test ends. Its channels are unbuffered, so sending twice
// returns only after Watch finished handling the first value.
func watch(t *testing.T, l *Loader) (tick chan time.Time, hup chan os.Signal) {
	t.Helper()
	tick, hup = make(chan time.Time), make(chan os.Signal)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		l.Watch(ctx, tick, hup)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return tick, hup
}

func TestWatchReloadsChangedFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	base := time.Unix(1_700_000_000, 0)
	certFile, keyFile := writePair(t, dir, 1, base)
	l, err := Load(certFile, keyFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	tick, _ := watch(t, l)
	settle := func() {
		tick <- time.Time{}
		tick <- time.Time{}
	}

	settle()
	if got := serial(t, l); got != 1 {
		t.Fatalf("serial = %d, want 1 (unchanged files)", got)
	}

	writePair(t, dir, 2, base.Add(time.Hour))
	settle()
	if got := serial(t, l); got != 2 {
		t.Errorf("serial = %d after renewal, want 2", got)
	}

	// A broken renewal keeps the working certificate.
	writeFile(t, certFile, []byte("half-written"), base.Add(2*time.Hour))
	settle()
	if got := serial(t, l); got != 2 {
		t.Errorf("serial = %d after a broken file, want 2", got)
	}
	// A missing file too.
	if err := os.Remove(keyFile); err != nil {
		t.Fatal(err)
	}
	settle()
	if got := serial(t, l); got != 2 {
		t.Errorf("serial = %d with the key missing, want 2", got)
	}

	writePair(t, dir, 3, base.Add(3*time.Hour))
	settle()
	if got := serial(t, l); got != 3 {
		t.Errorf("serial = %d after fixing the files, want 3", got)
	}
}

func TestWatchReloadsOnSignal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	base := time.Unix(1_700_000_000, 0)
	certFile, keyFile := writePair(t, dir, 1, base)
	l, err := Load(certFile, keyFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, hup := watch(t, l)
	// Same modification time and, by chance or not, same size: only the signal notices.
	writePair(t, dir, 2, base)
	hup <- syscall.SIGHUP
	hup <- syscall.SIGHUP
	if got := serial(t, l); got != 2 {
		t.Errorf("serial = %d after SIGHUP, want 2", got)
	}
}
