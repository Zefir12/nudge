package gitstacks

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"io"
	"runtime"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

const (
	testMaxObject = 1 << 20
	copyKiB       = 1 << 10
)

// testPack writes a pack by hand: git can't be made to send a delta like these.
type testPack struct {
	buf bytes.Buffer
}

func newTestPack(objects uint32) *testPack {
	p := &testPack{}
	p.buf.WriteString("PACK")
	_ = binary.Write(&p.buf, binary.BigEndian, uint32(2))
	_ = binary.Write(&p.buf, binary.BigEndian, objects)
	return p
}

func (p *testPack) object(t *testing.T, typ plumbing.ObjectType, size int, extra, data []byte) int64 {
	t.Helper()
	offset := int64(p.buf.Len())
	first := byte(typ)<<4 | byte(size&0xf)
	for size >>= 4; size > 0; size >>= 7 {
		p.buf.WriteByte(first | 0x80)
		first = byte(size & 0x7f)
	}
	p.buf.WriteByte(first)
	p.buf.Write(extra)
	z := zlib.NewWriter(&p.buf)
	if _, err := z.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return offset
}

func (p *testPack) blob(t *testing.T, data []byte) int64 {
	t.Helper()
	return p.object(t, plumbing.BlobObject, len(data), nil, data)
}

// delta adds a delta of the object at base, with the given header.
func (p *testPack) delta(t *testing.T, base int64, data []byte) {
	t.Helper()
	distance := int64(p.buf.Len()) - base
	ofs := []byte{byte(distance & 0x7f)}
	for distance >>= 7; distance > 0; distance >>= 7 {
		distance--
		ofs = append([]byte{0x80 | byte(distance&0x7f)}, ofs...)
	}
	p.object(t, plumbing.OFSDeltaObject, len(data), ofs, data)
}

func (p *testPack) finish() []byte {
	sum := sha1.Sum(p.buf.Bytes())
	return append(p.buf.Bytes(), sum[:]...)
}

// copies is a delta of a base of 1 KiB that copies all of it, n times.
func copies(n int) []byte {
	d := binary.AppendUvarint(nil, copyKiB)
	d = binary.AppendUvarint(d, uint64(n)*copyKiB)
	return append(d, bytes.Repeat([]byte{0x80 | 0x20, copyKiB >> 8}, n)...)
}

// deltaPack is a base of 1 KiB of zeros and a delta of it with the given data.
func deltaPack(t *testing.T, delta []byte) []byte {
	t.Helper()
	p := newTestPack(2)
	p.delta(t, p.blob(t, make([]byte, copyKiB)), delta)
	return p.finish()
}

func blobPack(t *testing.T, size int) []byte {
	t.Helper()
	p := newTestPack(1)
	p.blob(t, make([]byte, size))
	return p.finish()
}

// writeThrough writes a pack in pieces like a download and returns what ended it.
func writeThrough(w io.WriteCloser, pack []byte) error {
	var err error
	for rest := pack; len(rest) > 0 && err == nil; {
		n := min(len(rest), 4<<10)
		_, err = w.Write(rest[:n])
		rest = rest[n:]
	}
	if closeErr := w.Close(); err == nil {
		err = closeErr
	}
	return err
}

func TestPackChecker(t *testing.T) {
	t.Parallel()
	padded := binary.AppendUvarint(nil, copyKiB)
	padded = append(padded, bytes.Repeat([]byte{0x80}, 20)...)
	tests := []struct {
		name string
		pack []byte
		want error
	}{
		{name: "blob at the bound", pack: blobPack(t, testMaxObject)},
		{name: "blob past the bound", pack: blobPack(t, testMaxObject+1), want: errObjectTooLarge},
		{name: "delta at the bound", pack: deltaPack(t, copies(testMaxObject/copyKiB))},
		{name: "delta past the bound", pack: deltaPack(t, copies(testMaxObject/copyKiB+1)), want: errObjectTooLarge},
		{name: "padded delta size", pack: deltaPack(t, padded), want: errDeltaHeader},
		{name: "empty delta", pack: deltaPack(t, nil), want: errDeltaHeader},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			storage := &checkedStorage{
				Storage:   filesystem.NewStorage(memfs.New(), cache.NewObjectLRU(objectCache)),
				maxObject: testMaxObject,
			}
			w, err := storage.PackfileWriter()
			if err != nil {
				t.Fatal(err)
			}
			if err := writeThrough(w, tt.pack); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

// A pack that passes must still work: the delta's object can be read back.
func TestPackCheckerPassesAPackOn(t *testing.T) {
	t.Parallel()
	storage := &checkedStorage{
		Storage:   filesystem.NewStorage(memfs.New(), cache.NewObjectLRU(objectCache)),
		maxObject: testMaxObject,
	}
	w, err := storage.PackfileWriter()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeThrough(w, deltaPack(t, copies(4))); err != nil {
		t.Fatal(err)
	}
	want := make([]byte, 4*copyKiB)
	obj, err := storage.EncodedObject(plumbing.BlobObject, plumbing.ComputeHash(plumbing.BlobObject, want))
	if err != nil {
		t.Fatal(err)
	}
	r, err := obj.Reader()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("read %d bytes, err = %v", len(got), err)
	}
}

type recordingWriter struct {
	bytes.Buffer
	closed bool
}

func (r *recordingWriter) Close() error {
	r.closed = true
	return nil
}

// go-git inflates objects once it has them all, so a refused pack must stop short of that.
func TestPackCheckerHoldsBackTheTail(t *testing.T) {
	t.Parallel()
	bad := deltaPack(t, copies(testMaxObject/copyKiB+1)) // refused on its last object
	next := &recordingWriter{}
	if err := writeThrough(newPackChecker(next, testMaxObject), bad); !errors.Is(err, errObjectTooLarge) {
		t.Fatalf("err = %v", err)
	}
	if next.Len() > len(bad)-packTailBytes || !next.closed {
		t.Fatalf("passed on %d of %d bytes, closed %v", next.Len(), len(bad), next.closed)
	}
	good := blobPack(t, 1<<10)
	next = &recordingWriter{}
	if err := writeThrough(newPackChecker(next, testMaxObject), good); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(next.Bytes(), good) {
		t.Fatalf("passed on %d of %d bytes", next.Len(), len(good))
	}
}

// Not parallel: it counts what the whole process allocates. The target is 32 MiB, from a
// pack of a few hundred bytes that no limit on the pack's own size would catch.
func TestPackCheckerRefusesADeltaBomb(t *testing.T) {
	const target = 32 << 20
	pack := deltaPack(t, copies(target/copyKiB))
	storage := &checkedStorage{
		Storage:   filesystem.NewStorage(memfs.New(), cache.NewObjectLRU(objectCache)),
		maxObject: testMaxObject,
	}
	w, err := storage.PackfileWriter()
	if err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	err = writeThrough(w, pack)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, errObjectTooLarge) {
		t.Fatalf("err = %v", err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > target/2 {
		t.Fatalf("allocated %d bytes for a target of %d", allocated, target)
	}
}
