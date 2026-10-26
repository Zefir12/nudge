package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params are argon2id cost parameters. New hashes use the Service's params; a stored
// hash is verified with the params encoded in it, so they can change without a migration.
type Params struct {
	// Memory in KiB.
	Memory  uint32
	Time    uint32
	Threads uint8
}

// DefaultParams follow RFC 9106's second recommended option (64 MiB, 3 passes).
var DefaultParams = Params{Memory: 64 * 1024, Time: 3, Threads: 4}

const (
	saltLen = 16
	keyLen  = 32
	// maxMemory bounds what a stored hash can make verification allocate (1 GiB).
	maxMemory = 1 << 20
	maxTime   = 16
)

var errBadHash = errors.New("malformed password hash")

// hashPassword returns the PHC string `$argon2id$v=19$m=…,t=…,p=…$salt$key`.
func hashPassword(password string, p Params) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, keyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// verifyPassword reports whether password matches the encoded hash.
func verifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errBadHash
	}
	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return false, errBadHash
	}
	if p.Memory == 0 || p.Memory > maxMemory || p.Time == 0 || p.Time > maxTime || p.Threads == 0 {
		return false, errBadHash
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return false, errBadHash
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errBadHash
	}
	got := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, uint32(len(want))) // #nosec G115 -- decoded from a short string
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
