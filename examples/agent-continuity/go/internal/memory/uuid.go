package memory

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"time"
)

// newID returns a UUIDv7 of RFC 9562 with the same layout as the Python
// adapter: 48 bits of Unix milliseconds, version 7, variant 10 and 62 random
// bits. It is lexicographically ordered by time and safe as a db-ai path.
func newID(moment time.Time) string {
	ms := moment.UnixMilli()
	var random [8]byte
	// crypto/rand.Read never returns an error and always fills the buffer.
	_, _ = rand.Read(random[:])
	rnd := binary.BigEndian.Uint64(random[:])
	return fmt.Sprintf("%08x-%04x-7%03x-%04x-%012x",
		ms>>16, ms&0xFFFF, (rnd>>52)&0xFFF,
		uint16(0x8000)|uint16((rnd>>38)&0x3FFF),
		rnd&0xFFFFFFFFFFFF)
}
