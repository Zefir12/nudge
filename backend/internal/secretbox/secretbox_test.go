package secretbox

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	b1, err := LoadKey(dir)
	if err != nil {
		t.Fatalf("LoadKey: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || info.Size() != keySize {
		t.Errorf("key file mode %v size %d, want 0600 and %d", info.Mode().Perm(), info.Size(), keySize)
	}
	sealed := b1.Seal([]byte("hunter2"), "test")

	// A restart reads the same key back.
	b2, err := LoadKey(dir)
	if err != nil {
		t.Fatalf("LoadKey again: %v", err)
	}
	got, err := b2.Open(sealed, "test")
	if err != nil || string(got) != "hunter2" {
		t.Errorf("Open after reload = %q, %v", got, err)
	}
}

func TestLoadKeyBadFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, KeyFile), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKey(dir); err == nil {
		t.Error("LoadKey with a 5-byte key succeeded")
	}
	if _, err := LoadKey(filepath.Join(dir, "missing", "dir")); err == nil {
		t.Error("LoadKey in a missing directory succeeded")
	}
}

func TestSealOpen(t *testing.T) {
	t.Parallel()
	key := bytes.Repeat([]byte{1}, keySize)
	b, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	other, err := New(bytes.Repeat([]byte{2}, keySize))
	if err != nil {
		t.Fatal(err)
	}
	sealed := b.Seal([]byte("secret"), "registry password")
	if bytes.Contains(sealed, []byte("secret")) {
		t.Error("sealed value contains the plaintext")
	}
	if again := b.Seal([]byte("secret"), "registry password"); bytes.Equal(again, sealed) {
		t.Error("sealing twice gave the same value (nonce reused)")
	}
	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 1

	tests := []struct {
		name    string
		box     *Box
		sealed  []byte
		purpose string
		want    string
		wantErr bool
	}{
		{name: "ok", box: b, sealed: sealed, purpose: "registry password", want: "secret"},
		{name: "other purpose", box: b, sealed: sealed, purpose: "other", wantErr: true},
		{name: "other key", box: other, sealed: sealed, purpose: "registry password", wantErr: true},
		{name: "tampered", box: b, sealed: tampered, purpose: "registry password", wantErr: true},
		{name: "too short", box: b, sealed: sealed[:5], purpose: "registry password", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := tt.box.Open(tt.sealed, tt.purpose)
			if tt.wantErr {
				if !errors.Is(err, ErrDecrypt) {
					t.Errorf("Open err = %v, want ErrDecrypt", err)
				}
				return
			}
			if err != nil || string(got) != tt.want {
				t.Errorf("Open = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
	if _, err := New([]byte("short")); err == nil {
		t.Error("New with a short key succeeded")
	}
}
