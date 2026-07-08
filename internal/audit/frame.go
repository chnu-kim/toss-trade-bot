package audit

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
)

// A record is framed on disk as:
//
//	[len uint32 BE][crc32c uint32 BE][payload len bytes]
//
// The length bound plus the CRC give strong torn-tail detection: a crash that
// leaves a partially written frame is caught on read (short read, absurd length,
// or CRC mismatch) and the tail is discarded. A single write() + fsync of the
// whole frame is what "committed" means; anything past the last valid frame is
// treated as uncommitted (ADR-0006 point 3).

const (
	frameHeaderLen = 8 // 4-byte length + 4-byte CRC
	// maxRecordSize bounds a single record so a torn length header cannot make
	// recovery try to read gigabytes. Audit records are small JSON objects.
	//
	// This is the SAME bound the writer enforces at commit time (writer.go
	// commit, before encodeFrame) — sharing one constant makes "a record that
	// was accepted at write time is always readable on recovery" an invariant
	// rather than something that can drift between the two call sites (issue
	// #25 / ADR-0006 point 4).
	maxRecordSize = 8 << 20 // 8 MiB
)

// maxRecordSizeFitsUint32 is a compile-time guarantee that maxRecordSize always
// fits the uint32 length field encodeFrame writes. If maxRecordSize is ever
// widened past math.MaxUint32 this line fails to compile, catching the
// uint32(len(payload)) truncation risk noted in issue #25 before it can ship.
const maxRecordSizeFitsUint32 uint32 = maxRecordSize

// crc32cTable is the Castagnoli polynomial table (hardware-accelerated on most
// CPUs), used for frame integrity.
var crc32cTable = crc32.MakeTable(crc32.Castagnoli)

// errTornFrame signals that the bytes at the current offset are not a complete,
// intact frame — a torn tail (or corruption). Recovery stops here.
var errTornFrame = errors.New("audit: torn or corrupt frame")

// encodeFrame wraps payload in a length+CRC frame.
func encodeFrame(payload []byte) []byte {
	frame := make([]byte, frameHeaderLen+len(payload))
	binary.BigEndian.PutUint32(frame[0:4], uint32(len(payload)))
	binary.BigEndian.PutUint32(frame[4:8], crc32.Checksum(payload, crc32cTable))
	copy(frame[frameHeaderLen:], payload)
	return frame
}

// readFrame reads one frame from r. It returns the payload and the total number
// of bytes the frame occupied. A short read, an out-of-bound length, or a CRC
// mismatch returns errTornFrame — the caller treats it as the end of committed
// data. A clean EOF at a frame boundary returns io.EOF.
func readFrame(r io.Reader) (payload []byte, n int, err error) {
	var header [frameHeaderLen]byte
	hn, herr := io.ReadFull(r, header[:])
	if herr == io.EOF {
		return nil, 0, io.EOF // clean boundary
	}
	if herr == io.ErrUnexpectedEOF || (herr == nil && hn < frameHeaderLen) {
		return nil, hn, errTornFrame // partial header
	}
	if herr != nil {
		return nil, hn, herr
	}

	length := binary.BigEndian.Uint32(header[0:4])
	if length == 0 || length > maxRecordSize {
		return nil, frameHeaderLen, errTornFrame
	}
	want := binary.BigEndian.Uint32(header[4:8])

	payload = make([]byte, length)
	pn, perr := io.ReadFull(r, payload)
	if perr != nil {
		return nil, frameHeaderLen + pn, errTornFrame // short payload
	}
	if crc32.Checksum(payload, crc32cTable) != want {
		return nil, frameHeaderLen + pn, errTornFrame
	}
	return payload, frameHeaderLen + int(length), nil
}
