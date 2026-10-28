package backup

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
)

// An encrypted archive is a header followed by chunks sealed with AES-256-GCM (the STREAM
// construction): every chunk's nonce is the header's prefix, the chunk's number and a
// flag set only on the last one, so chunks can't be reordered, dropped or cut off
// unnoticed. The header is every chunk's associated data.
//
//	magic "NUDGEBAK" | format 1 | argon2id time (u32) | memory KiB (u32) | threads (u8) | salt (16) | nonce prefix (7)
const (
	magic       = "NUDGEBAK"
	format      = 1
	saltSize    = 16
	prefixSize  = 7
	headerSize  = len(magic) + 1 + 4 + 4 + 1 + saltSize + prefixSize
	chunkSize   = 64 << 10
	tagSize     = 16
	keySize     = 32
	maxChunks   = 1<<32 - 1
	lastChunk   = 1
	otherChunks = 0
)

// The bounds a header's key derivation may ask for, so a crafted archive can't make a
// restore allocate gigabytes or spin for minutes.
const (
	maxKDFTime   = 10
	maxKDFMemory = 1 << 20 // KiB: 1 GiB
)

// ErrDecrypt means a wrong passphrase or a damaged or truncated archive: GCM can't tell
// them apart.
var ErrDecrypt = errors.New("wrong passphrase, or the archive is damaged")

// ErrNotBackup means the file doesn't start like a nudge backup.
var ErrNotBackup = errors.New("not a nudge backup")

// kdf are argon2id's parameters for the archive key.
type kdf struct {
	Time    uint32
	Memory  uint32 // KiB
	Threads uint8
}

// defaultKDF is argon2id's recommendation for interactive use, a little stronger: a
// backup is made and opened rarely.
var defaultKDF = kdf{Time: 3, Memory: 64 << 10, Threads: 4}

type sealer struct {
	w      io.Writer
	aead   cipher.AEAD
	header []byte
	buf    []byte
	n      uint32
	err    error
	closed bool
}

// encrypt returns a writer that seals what is written to it into w; Close seals the last
// chunk and must be called, or the archive is unreadable.
func encrypt(w io.Writer, passphrase string, p kdf) (io.WriteCloser, error) {
	header := make([]byte, 0, headerSize)
	header = append(header, magic...)
	header = append(header, format)
	header = binary.BigEndian.AppendUint32(header, p.Time)
	header = binary.BigEndian.AppendUint32(header, p.Memory)
	header = append(header, p.Threads)
	salt := make([]byte, saltSize+prefixSize)
	_, _ = rand.Read(salt)
	header = append(header, salt...)
	aead, err := newAEAD(passphrase, header, p)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(header); err != nil {
		return nil, fmt.Errorf("write backup: %w", err)
	}
	return &sealer{w: w, aead: aead, header: header, buf: make([]byte, 0, chunkSize)}, nil
}

func (s *sealer) Write(p []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	written := 0
	for len(p) > 0 {
		// A full chunk is sealed only once more data follows: if none does, Close seals
		// it as the last one.
		if len(s.buf) == chunkSize {
			if s.err = s.seal(otherChunks); s.err != nil {
				return written, s.err
			}
		}
		n := copy(s.buf[len(s.buf):chunkSize], p)
		s.buf = s.buf[:len(s.buf)+n]
		p = p[n:]
		written += n
	}
	return written, nil
}

func (s *sealer) Close() error {
	if s.closed {
		return s.err
	}
	s.closed = true
	if s.err != nil {
		return s.err
	}
	s.err = s.seal(lastChunk)
	return s.err
}

func (s *sealer) seal(last byte) error {
	if s.n == maxChunks {
		return errors.New("write backup: archive too large")
	}
	out := s.aead.Seal(s.buf[:0:0], nonce(s.header, s.n, last), s.buf, s.header)
	s.n++
	s.buf = s.buf[:0]
	if _, err := s.w.Write(out); err != nil {
		return fmt.Errorf("write backup: %w", err)
	}
	return nil
}

type opener struct {
	r      *bufio.Reader
	aead   cipher.AEAD
	header []byte
	in     []byte
	out    []byte
	n      uint32
	done   bool
	err    error
}

// decrypt reads an archive's header from r and returns a reader of its plaintext. A read
// fails with ErrDecrypt as soon as a chunk doesn't open, and the reader reports io.EOF
// only after the last chunk, so a truncated archive never reads as a complete one.
func decrypt(r io.Reader, passphrase string) (io.Reader, error) {
	header := make([]byte, headerSize)
	if _, err := io.ReadFull(r, header); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, ErrNotBackup
		}
		return nil, fmt.Errorf("read backup: %w", err)
	}
	if string(header[:len(magic)]) != magic {
		return nil, ErrNotBackup
	}
	if v := header[len(magic)]; v != format {
		return nil, fmt.Errorf("backup format %d is not supported by this nudge", v)
	}
	at := len(magic) + 1
	p := kdf{
		Time:    binary.BigEndian.Uint32(header[at:]),
		Memory:  binary.BigEndian.Uint32(header[at+4:]),
		Threads: header[at+8],
	}
	if p.Time == 0 || p.Time > maxKDFTime || p.Memory < 8*uint32(max(p.Threads, 1)) || p.Memory > maxKDFMemory || p.Threads == 0 {
		return nil, ErrNotBackup
	}
	aead, err := newAEAD(passphrase, header, p)
	if err != nil {
		return nil, err
	}
	return &opener{r: bufio.NewReaderSize(r, chunkSize+tagSize+1), aead: aead, header: header, in: make([]byte, chunkSize+tagSize)}, nil
}

func (o *opener) Read(p []byte) (int, error) {
	for len(o.out) == 0 {
		if o.err != nil {
			return 0, o.err
		}
		if o.done {
			return 0, io.EOF
		}
		o.err = o.open()
	}
	n := copy(p, o.out)
	o.out = o.out[n:]
	return n, nil
}

// open reads and opens the next chunk; a chunk is the last one when nothing follows it.
func (o *opener) open() error {
	n, err := io.ReadFull(o.r, o.in)
	switch {
	case errors.Is(err, io.EOF):
		// The chunk before was full and not the last: the file was cut there.
		return ErrDecrypt
	case errors.Is(err, io.ErrUnexpectedEOF):
	case err != nil:
		return fmt.Errorf("read backup: %w", err)
	}
	last := byte(otherChunks)
	if n < len(o.in) {
		last = lastChunk
	} else if _, err := o.r.Peek(1); errors.Is(err, io.EOF) {
		last = lastChunk
	} else if err != nil {
		return fmt.Errorf("read backup: %w", err)
	}
	if n < tagSize || o.n == maxChunks {
		return ErrDecrypt
	}
	out, err := o.aead.Open(o.in[:0], nonce(o.header, o.n, last), o.in[:n], o.header)
	if err != nil {
		return ErrDecrypt
	}
	o.n++
	o.out = out
	o.done = last == lastChunk
	return nil
}

func newAEAD(passphrase string, header []byte, p kdf) (cipher.AEAD, error) {
	salt := header[headerSize-saltSize-prefixSize : headerSize-prefixSize]
	key := argon2.IDKey([]byte(passphrase), salt, p.Time, p.Memory, p.Threads, keySize)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("backup key: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("backup key: %w", err)
	}
	return aead, nil
}

func nonce(header []byte, n uint32, last byte) []byte {
	b := make([]byte, 0, prefixSize+4+1)
	b = append(b, header[headerSize-prefixSize:]...)
	b = binary.BigEndian.AppendUint32(b, n)
	return append(b, last)
}
