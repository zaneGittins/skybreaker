package gitproto

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"slices"
	"sort"
	"strconv"
)

// Reading a pushed pack for its commits. Every object is inflated to find
// where the next one starts; commits, and deltas whose base is a commit,
// are kept so their messages can be checked. The pack must be
// self-contained: a delta against an object outside it (a thin pack) could
// be a commit nobody can read here, so it is refused. The broker asks git
// not to send thin packs (NoThin), so only a client that ignores that is
// refused.

const (
	objCommit   = 1
	objTree     = 2
	objBlob     = 3
	objTag      = 4
	objOfsDelta = 6
	objRefDelta = 7

	// The limits bound what one push can cost the broker: memory for each
	// object's place in the pack (9 bytes), the commits kept, and the work
	// of inflating everything.
	maxObjects     = 10_000_000
	maxObjectSize  = 1 << 40
	maxCommitSize  = 8 << 20
	maxCommitTotal = 256 << 20
	maxInflated    = 16 << 30
)

// ErrThinPack is a push with a delta against an object outside the pack.
var ErrThinPack = errors.New("the push is a thin pack (deltas against objects outside it), which can't be checked; push again with git push --no-thin")

var errTrailing = errors.New("data after the pack")

// Push is a receive-pack request body, read whole.
type Push struct {
	Updates []RefUpdate
	Caps    []string
}

// Commit is one commit in a pushed pack.
type Commit struct {
	OID     string
	Message string
}

// ReadPush reads a whole receive-pack request body: the commands, any push
// options, and the pack, calling commit for each commit in it. It fails on
// anything it can't read completely, including a bad pack checksum or bytes
// after the pack; commit may return an error to stop.
//
// safe, when set, is told how much of the body has been read inside the
// pack, short of its checksum. Those bytes can go upstream before the
// verdict: without the checksum, and so without the rest, a server takes
// none of the push. Nothing is reported for a body without a pack (a probe,
// or deletes only), which a server could act on as soon as it had it.
func ReadPush(r io.Reader, commit func(Commit) error, safe func(n int64)) (Push, error) {
	s := &stream{r: r, buf: make([]byte, 64<<10)}
	ups, caps, err := ReadCommands(s)
	if errors.Is(err, errNoCommands) {
		// git's probe: a bare flush-pkt and nothing else.
		return Push{}, s.end()
	}
	if err != nil {
		return Push{}, err
	}
	p := Push{Updates: ups, Caps: caps}
	if slices.Contains(caps, "push-options") {
		for n := 0; ; n++ {
			_, flush, err := readPkt(s)
			if err != nil {
				return p, fmt.Errorf("push options: %w", err)
			}
			if flush {
				break
			}
			if n == maxCommands {
				return p, errors.New("too many push options")
			}
		}
	}
	// A push of deletes only has no pack.
	if !slices.ContainsFunc(ups, func(u RefUpdate) bool { return !u.IsDelete() }) {
		return p, s.end()
	}
	h := sha1.New
	if len(ups[0].New) == 64 {
		h = sha256.New
	}
	s.report = safe
	return p, readPack(s, h, commit)
}

// stream is the body as the parser reads it: buffered, counting what it
// has consumed, and hashing it while sum is set (the pack, for its
// checksum). It is an io.ByteReader, so zlib reads exactly one object's
// stream from it and no further.
type stream struct {
	r        io.Reader
	buf      []byte
	pos, lim int
	err      error
	n        int64         // bytes consumed
	sum      hash.Hash     // nil: not hashing
	from     int           // where in buf hashing has reached
	report   func(n int64) // told n before each refill, in the pack until its checksum
}

// fill makes sure there's a byte to read, reporting progress and hashing
// what was consumed before the buffer is reused.
func (s *stream) fill() bool {
	if s.pos < s.lim {
		return true
	}
	if s.report != nil {
		s.report(s.n)
	}
	s.hash()
	s.pos, s.lim, s.from = 0, 0, 0
	for s.lim == 0 && s.err == nil {
		s.lim, s.err = s.r.Read(s.buf)
	}
	return s.lim > 0
}

func (s *stream) hash() {
	if s.sum != nil {
		s.sum.Write(s.buf[s.from:s.pos])
	}
	s.from = s.pos
}

func (s *stream) ReadByte() (byte, error) {
	if !s.fill() {
		return 0, s.err
	}
	c := s.buf[s.pos]
	s.pos++
	s.n++
	return c, nil
}

func (s *stream) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	if !s.fill() {
		return 0, s.err
	}
	n := copy(b, s.buf[s.pos:s.lim])
	s.pos += n
	s.n += int64(n)
	return n, nil
}

// end requires the body to end here.
func (s *stream) end() error {
	switch _, err := s.ReadByte(); {
	case err == io.EOF:
		return nil
	case err != nil:
		return err
	}
	return errTrailing
}

func readPack(s *stream, newHash func() hash.Hash, commit func(Commit) error) error {
	s.hash()
	s.sum = newHash()
	var hdr [12]byte
	if _, err := io.ReadFull(s, hdr[:]); err != nil {
		return fmt.Errorf("pack header: %w", err)
	}
	if string(hdr[:4]) != "PACK" {
		return errors.New("no pack after the commands")
	}
	if v := binary.BigEndian.Uint32(hdr[4:8]); v != 2 && v != 3 {
		return fmt.Errorf("pack version %d", v)
	}
	count := binary.BigEndian.Uint32(hdr[8:12])
	if count > maxObjects {
		return fmt.Errorf("pack of %d objects is over %d", count, maxObjects)
	}
	var (
		offsets  []int64 // where each object starts, ascending
		types    []byte  // each object's type, deltas resolved
		commits  = map[int64][]byte{}
		held     int
		inflated int64
	)
	for i := uint32(0); i < count; i++ {
		start := s.n
		typ, size, err := objectHeader(s)
		if err != nil {
			return fmt.Errorf("object %d: %w", i, err)
		}
		var base []byte // a commit this delta applies to
		switch typ {
		case objCommit, objTree, objBlob, objTag:
		case objOfsDelta:
			off, err := ofsOffset(s)
			if err != nil {
				return fmt.Errorf("object %d: %w", i, err)
			}
			at := start - off
			j := sort.Search(len(offsets), func(k int) bool { return offsets[k] >= at })
			if off <= 0 || j == len(offsets) || offsets[j] != at {
				return fmt.Errorf("object %d: delta base is not an object in the pack", i)
			}
			typ = types[j]
			if typ == objCommit {
				base = commits[at]
			}
		case objRefDelta:
			return ErrThinPack
		default:
			return fmt.Errorf("object %d: bad type %d", i, typ)
		}
		if inflated += size; inflated > maxInflated {
			return errors.New("the push inflates to too much to check")
		}
		keep := typ == objCommit
		if keep && size > maxCommitSize {
			return fmt.Errorf("object %d: a commit of %d bytes is too large to check", i, size)
		}
		data, err := inflate(s, size, keep)
		if err != nil {
			return fmt.Errorf("object %d: %w", i, err)
		}
		if base != nil {
			if data, err = applyDelta(base, data); err != nil {
				return fmt.Errorf("object %d: %w", i, err)
			}
		}
		offsets = append(offsets, start)
		types = append(types, typ)
		if !keep {
			continue
		}
		if held += len(data); held > maxCommitTotal {
			return errors.New("the push's commits are too large to check")
		}
		commits[start] = data
		if err := commit(Commit{OID: objectID(newHash, "commit", data), Message: message(data)}); err != nil {
			return err
		}
	}
	// From here on nothing is safe to send: the checksum completes the
	// pack.
	s.report = nil
	s.hash()
	want := s.sum.Sum(nil)
	s.sum = nil
	got := make([]byte, len(want))
	if _, err := io.ReadFull(s, got); err != nil {
		return fmt.Errorf("pack checksum: %w", err)
	}
	if !bytes.Equal(got, want) {
		return errors.New("pack checksum doesn't match")
	}
	return s.end()
}

// objectHeader reads an object's type and inflated size.
func objectHeader(r io.ByteReader) (typ byte, size int64, err error) {
	c, err := r.ReadByte()
	if err != nil {
		return 0, 0, err
	}
	typ, size = c>>4&7, int64(c&15)
	for shift := 4; c&0x80 != 0; shift += 7 {
		if c, err = r.ReadByte(); err != nil {
			return 0, 0, err
		}
		if shift > 40 {
			return 0, 0, errors.New("object size too large")
		}
		size |= int64(c&0x7f) << shift
	}
	if size > maxObjectSize {
		return 0, 0, errors.New("object size too large")
	}
	return typ, size, nil
}

// ofsOffset reads an OFS_DELTA's distance back to its base.
func ofsOffset(r io.ByteReader) (int64, error) {
	c, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	off := int64(c & 0x7f)
	for n := 0; c&0x80 != 0; n++ {
		if n > 8 {
			return 0, errors.New("delta offset too large")
		}
		if c, err = r.ReadByte(); err != nil {
			return 0, err
		}
		off = (off+1)<<7 | int64(c&0x7f)
	}
	return off, nil
}

// inflate reads one zlib stream of exactly size bytes, returning them when
// keep is set and discarding them otherwise.
func inflate(r *stream, size int64, keep bool) ([]byte, error) {
	z, err := zlib.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("inflating: %w", err)
	}
	var buf bytes.Buffer
	var dst io.Writer = io.Discard
	if keep {
		buf.Grow(int(size))
		dst = &buf
	}
	n, err := io.Copy(dst, io.LimitReader(z, size+1))
	if err != nil {
		return nil, fmt.Errorf("inflating: %w", err)
	}
	if n != size {
		return nil, fmt.Errorf("object is %d bytes, not the %d its header says", n, size)
	}
	// Reading to the end checks the stream's checksum.
	if _, err := z.Read(make([]byte, 1)); err != io.EOF {
		return nil, errors.New("object's compressed stream doesn't end where it should")
	}
	return buf.Bytes(), nil
}

// applyDelta rebuilds an object from its base and a git delta.
func applyDelta(base, delta []byte) ([]byte, error) {
	bad := errors.New("malformed delta")
	d := bytes.NewReader(delta)
	varint := func() (int64, error) {
		var v int64
		for shift := 0; ; shift += 7 {
			c, err := d.ReadByte()
			if err != nil || shift > 56 {
				return 0, bad
			}
			v |= int64(c&0x7f) << shift
			if c&0x80 == 0 {
				return v, nil
			}
		}
	}
	baseLen, err := varint()
	if err != nil || baseLen != int64(len(base)) {
		return nil, bad
	}
	outLen, err := varint()
	if err != nil || outLen > maxCommitSize {
		return nil, bad
	}
	out := make([]byte, 0, outLen)
	for d.Len() > 0 {
		op, _ := d.ReadByte()
		switch {
		case op&0x80 != 0:
			var off, n int64
			for i := 0; i < 4; i++ {
				if op&(1<<i) != 0 {
					c, err := d.ReadByte()
					if err != nil {
						return nil, bad
					}
					off |= int64(c) << (8 * i)
				}
			}
			for i := 0; i < 3; i++ {
				if op&(0x10<<i) != 0 {
					c, err := d.ReadByte()
					if err != nil {
						return nil, bad
					}
					n |= int64(c) << (8 * i)
				}
			}
			if n == 0 {
				n = 0x10000
			}
			if off+n > int64(len(base)) || int64(len(out))+n > outLen {
				return nil, bad
			}
			out = append(out, base[off:off+n]...)
		case op != 0:
			if int(op) > d.Len() || int64(len(out))+int64(op) > outLen {
				return nil, bad
			}
			chunk := make([]byte, op)
			d.Read(chunk)
			out = append(out, chunk...)
		default:
			return nil, bad
		}
	}
	if int64(len(out)) != outLen {
		return nil, bad
	}
	return out, nil
}

func objectID(newHash func() hash.Hash, typ string, data []byte) string {
	h := newHash()
	h.Write([]byte(typ + " " + strconv.Itoa(len(data)) + "\x00"))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// message is a commit's message: everything after the blank line that ends
// its headers.
func message(commit []byte) string {
	if i := bytes.Index(commit, []byte("\n\n")); i >= 0 {
		return string(commit[i+2:])
	}
	return ""
}
