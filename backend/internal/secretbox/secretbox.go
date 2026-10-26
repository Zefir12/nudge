// Package secretbox encrypts secrets nudge must read back (registry passwords), unlike
// the ones it only checks, which are hashed. The key lives in the data directory, so a
// copy of the database alone doesn't reveal them.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// KeyFile is the key's file name in the data directory.
const KeyFile = "secret.key"

const keySize = 32

// ErrDecrypt means a sealed value was tampered with, sealed for another purpose or with
// another key.
var ErrDecrypt = errors.New("decrypt: wrong key or corrupted value")

// Box seals and opens values with AES-256-GCM.
type Box struct {
	aead cipher.AEAD
}

// LoadKey reads the key from dir/secret.key, creating it (0600) with a random key if it
// doesn't exist.
func LoadKey(dir string) (*Box, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("secret key: %w", err)
	}
	defer func() { _ = root.Close() }()
	key, err := root.ReadFile(KeyFile)
	if errors.Is(err, fs.ErrNotExist) {
		key, err = createKey(root)
	}
	if err != nil {
		return nil, fmt.Errorf("secret key: %w", err)
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("secret key %s: want %d bytes, got %d", filepath.Join(dir, KeyFile), keySize, len(key))
	}
	return New(key)
}

// createKey writes a new random key to a temporary file and links it into place, so a
// crash never leaves a short key behind and a concurrent start can't replace a key
// already in use (the link fails if the key exists; that key is used instead).
func createKey(root *os.Root) ([]byte, error) {
	key := make([]byte, keySize)
	_, _ = rand.Read(key)
	tmp := fmt.Sprintf("%s.%x.tmp", KeyFile, key[:4])
	defer func() { _ = root.Remove(tmp) }()
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(key); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := root.Link(tmp, KeyFile); errors.Is(err, fs.ErrExist) {
		return root.ReadFile(KeyFile)
	} else if err != nil {
		return nil, err
	}
	return key, nil
}

// New returns a Box for a 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("secret key: want %d bytes, got %d", keySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secret key: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secret key: %w", err)
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext for purpose (such as "registry password"), which Open must be
// given again; the random nonce is prepended to the result.
func (b *Box) Seal(plaintext []byte, purpose string) []byte {
	nonce := make([]byte, b.aead.NonceSize(), b.aead.NonceSize()+len(plaintext)+b.aead.Overhead())
	_, _ = rand.Read(nonce)
	return b.aead.Seal(nonce, nonce, plaintext, []byte(purpose))
}

// Open decrypts a value from Seal.
func (b *Box) Open(sealed []byte, purpose string) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n+b.aead.Overhead() {
		return nil, ErrDecrypt
	}
	plaintext, err := b.aead.Open(nil, sealed[:n], sealed[n:], []byte(purpose))
	if err != nil {
		return nil, ErrDecrypt
	}
	return plaintext, nil
}
