package backup

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// testKDF keeps the tests fast; the format is the same.
var testKDF = kdf{Time: 1, Memory: 64, Threads: 1}

func sealed(t *testing.T, plain []byte, pass string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := encrypt(&buf, pass, testKDF)
	if err != nil {
		t.Fatal(err)
	}
	// Uneven writes cross chunk boundaries.
	for p := plain; len(p) > 0; {
		n := min(len(p), 1000)
		if _, err := w.Write(p[:n]); err != nil {
			t.Fatal(err)
		}
		p = p[n:]
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func opened(data []byte, pass string) ([]byte, error) {
	r, err := decrypt(bytes.NewReader(data), pass)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func TestCryptRoundTrip(t *testing.T) {
	t.Parallel()
	for _, size := range []int{0, 1, chunkSize - 1, chunkSize, chunkSize + 1, 3 * chunkSize, 3*chunkSize + 17} {
		plain := make([]byte, size)
		_, _ = rand.Read(plain)
		got, err := opened(sealed(t, plain, "correct horse"), "correct horse")
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("size %d: plaintext differs", size)
		}
	}
}

func TestCryptRefuses(t *testing.T) {
	t.Parallel()
	plain := make([]byte, 2*chunkSize+100)
	_, _ = rand.Read(plain)
	data := sealed(t, plain, "correct horse")
	record := chunkSize + tagSize
	edit := func(f func([]byte) []byte) []byte { return f(bytes.Clone(data)) }
	hugeMemory := edit(func(b []byte) []byte {
		binary.BigEndian.PutUint32(b[len(magic)+1+4:], maxKDFMemory+1)
		return b
	})
	tests := []struct {
		name string
		data []byte
		pass string
		want error
	}{
		{"wrong passphrase", data, "wrong horse", ErrDecrypt},
		{"cut after a full chunk", data[:headerSize+record], "correct horse", ErrDecrypt},
		{"cut after two full chunks", data[:headerSize+2*record], "correct horse", ErrDecrypt},
		{"cut inside a chunk", data[:headerSize+record+10], "correct horse", ErrDecrypt},
		{"cut inside the last chunk", data[:len(data)-1], "correct horse", ErrDecrypt},
		{"extra byte", append(bytes.Clone(data), 0), "correct horse", ErrDecrypt},
		{"flipped byte", edit(func(b []byte) []byte { b[headerSize+5] ^= 1; return b }), "correct horse", ErrDecrypt},
		{"changed header", edit(func(b []byte) []byte { b[headerSize-1] ^= 1; return b }), "correct horse", ErrDecrypt},
		{"swapped chunks", edit(func(b []byte) []byte {
			first := bytes.Clone(b[headerSize : headerSize+record])
			copy(b[headerSize:], b[headerSize+record:headerSize+2*record])
			copy(b[headerSize+record:], first)
			return b
		}), "correct horse", ErrDecrypt},
		{"header only", data[:headerSize], "correct horse", ErrDecrypt},
		{"short header", data[:10], "correct horse", ErrNotBackup},
		{"other file", []byte("PK\x03\x04 a zip file, not a backup at all......"), "correct horse", ErrNotBackup},
		{"huge key derivation", hugeMemory, "correct horse", ErrNotBackup},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := opened(tt.data, tt.pass); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}
