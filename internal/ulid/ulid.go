// Package ulid generates ULIDs: 48-bit unix millisecond timestamp + 80 bits
// of crypto randomness, Crockford base32, 26 chars. Monotonic within a
// millisecond via a per-process counter; cross-process uniqueness from entropy.
package ulid

import (
	"crypto/rand"
	"fmt"
	"sync"
	"time"
)

const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var (
	mu      sync.Mutex
	lastMS  uint64
	lastEnt [10]byte
)

// New returns a new ULID string for t.
func New(t time.Time) string {
	ms := uint64(t.UnixMilli())
	var ent [10]byte
	// crypto/rand failures are unrecoverable; read before taking the lock.
	if _, err := rand.Read(ent[:]); err != nil {
		panic(fmt.Sprintf("ulid: entropy source failed: %v", err))
	}
	mu.Lock()
	if ms == lastMS {
		increment(&lastEnt)
		ent = lastEnt
	} else {
		lastMS, lastEnt = ms, ent
	}
	mu.Unlock()

	var b [16]byte
	b[0], b[1], b[2], b[3], b[4], b[5] = byte(ms>>40), byte(ms>>32), byte(ms>>24), byte(ms>>16), byte(ms>>8), byte(ms)
	copy(b[6:], ent[:])
	return encode(&b)
}

func increment(e *[10]byte) {
	for i := len(e) - 1; i >= 0; i-- {
		e[i]++
		if e[i] != 0 {
			return
		}
	}
}

// encode renders 128 bits as 26 base32 chars (130 bits, top 2 zero-padded).
func encode(b *[16]byte) string {
	var out [26]byte
	bits, val, n := 0, 0, 0
	for _, x := range b {
		val = val<<8 | int(x)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out[n] = alphabet[(val>>bits)&31]
			val &= (1 << bits) - 1 // keep only unextracted bits; val must stay small
			n++
		}
	}
	if bits > 0 { // 3 leftover bits, left-aligned into the final char
		out[n] = alphabet[(val<<(5-bits))&31]
		n++
	}
	for n < 26 {
		out[n] = alphabet[0]
		n++
	}
	return string(out[:])
}
