package gitstacks

import (
	"encoding/binary"
	"errors"
	"io"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

const (
	// packTailBytes is held back from go-git until a pack has passed: the checksum that
	// ends a pack and the last byte of the last object.
	packTailBytes = 20 + 1
	// deltaHeaderBytes holds a delta's two sizes, at most 10 bytes each.
	deltaHeaderBytes = 20
)

var errDeltaHeader = errors.New("unreadable delta header")

// checkedStorage is a clone's storage, with the pack checked on its way in. To index a
// pack, go-git reads every object into memory, however little of the pack it takes up.
type checkedStorage struct {
	*filesystem.Storage
	maxObject int64
}

// PackfileWriter is the Storage's PackfileWriter, checking what it is given.
func (s *checkedStorage) PackfileWriter() (io.WriteCloser, error) {
	w, err := s.Storage.PackfileWriter()
	if err != nil {
		return nil, err
	}
	return newPackChecker(w, s.maxObject), nil
}

// packChecker passes a pack on to go-git while checking it, without keeping it in memory.
// go-git only starts inflating objects into memory once it has read every object, and takes
// a pack that ends right after them as complete. So the pack's tail, which cuts the last
// object short, is held back until the whole pack has passed.
type packChecker struct {
	next   io.WriteCloser
	pipe   *io.PipeWriter
	result chan error
	tail   []byte
	failed bool
}

func newPackChecker(next io.WriteCloser, maxObject int64) *packChecker {
	r, w := io.Pipe()
	c := &packChecker{next: next, pipe: w, result: make(chan error, 1)}
	go func() {
		err := checkPack(r, maxObject)
		if err == nil {
			_, err = io.Copy(io.Discard, r) // the checksum
		}
		_ = r.CloseWithError(err)
		c.result <- err
	}()
	return c
}

// Write checks p and passes it on, except for what may be the tail.
func (c *packChecker) Write(p []byte) (int, error) {
	if _, err := c.pipe.Write(p); err != nil {
		c.failed = true
		return 0, err
	}
	c.tail = append(c.tail, p...)
	if pass := len(c.tail) - packTailBytes; pass > 0 {
		if _, err := c.next.Write(c.tail[:pass]); err != nil {
			c.failed = true
			return 0, err
		}
		c.tail = append(c.tail[:0], c.tail[pass:]...)
	}
	return len(p), nil
}

// Close finishes the check. Only a pack that passed gets its tail; without it, go-git gives
// up on the pack instead of inflating its objects.
func (c *packChecker) Close() error {
	_ = c.pipe.Close()
	err := <-c.result
	if err == nil && !c.failed {
		_, err = c.next.Write(c.tail)
	}
	if closeErr := c.next.Close(); err == nil {
		err = closeErr
	}
	return err
}

// checkPack reads a pack and fails if an object inflates to more than maxObject. A delta's
// inflated size is its own target's, which a few bytes of instructions can make far larger.
func checkPack(r io.Reader, maxObject int64) error {
	scanner := packfile.NewScanner(r)
	_, count, err := scanner.Header()
	if err != nil {
		return err
	}
	for i := uint32(0); i < count; i++ {
		h, err := scanner.NextObjectHeader()
		if err != nil {
			return err
		}
		// A negative size would turn off the scanner's check of the size against the data.
		if h.Length < 0 || h.Length > maxObject {
			return errObjectTooLarge
		}
		var delta deltaHeader
		sink := io.Discard
		isDelta := h.Type == plumbing.OFSDeltaObject || h.Type == plumbing.REFDeltaObject
		if isDelta {
			sink = &delta
		}
		if _, _, err := scanner.NextObject(sink); err != nil {
			return err
		}
		if isDelta {
			target, err := delta.targetSize()
			if err != nil {
				return err
			}
			if target > uint64(maxObject) { // #nosec G115 -- a positive bound
				return errObjectTooLarge
			}
		}
	}
	return nil
}

// deltaHeader keeps the start of a delta, which is the size of its base and of its target.
type deltaHeader struct {
	b [deltaHeaderBytes]byte
	n int
}

// Write keeps what fits and drops the rest.
func (d *deltaHeader) Write(p []byte) (int, error) {
	d.n += copy(d.b[d.n:], p)
	return len(p), nil
}

// targetSize decodes the second size. go-git accepts sizes padded with any number of bytes,
// which git never writes; those are refused as they couldn't be checked.
func (d *deltaHeader) targetSize() (uint64, error) {
	b := d.b[:d.n]
	_, n := binary.Uvarint(b)
	if n <= 0 {
		return 0, errDeltaHeader
	}
	target, n := binary.Uvarint(b[n:])
	if n <= 0 {
		return 0, errDeltaHeader
	}
	return target, nil
}
