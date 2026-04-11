package phos

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"sync/atomic"
	"time"
)

var fallbackSeq atomic.Uint64

func generateTraceID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fallbackTraceID()
	}

	var encoded [32]byte
	hex.Encode(encoded[:], raw[:])
	return string(encoded[:])
}

func generateSpanID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fallbackSpanID()
	}

	var encoded [16]byte
	hex.Encode(encoded[:], raw[:])
	return string(encoded[:])
}

func fallbackTraceID() string {
	n := fallbackSeq.Add(1)
	t := uint64(time.Now().UnixNano())
	var raw [16]byte
	binary.BigEndian.PutUint64(raw[0:8], t)
	binary.BigEndian.PutUint64(raw[8:16], n)
	var encoded [32]byte
	hex.Encode(encoded[:], raw[:])
	return string(encoded[:])
}

func fallbackSpanID() string {
	n := fallbackSeq.Add(1)
	t := uint64(time.Now().UnixNano())
	var raw [8]byte
	// | n avoids all-zero bytes when t^n collapses (invalid for W3C parent id).
	binary.BigEndian.PutUint64(raw[:], t|n)
	var encoded [16]byte
	hex.Encode(encoded[:], raw[:])
	return string(encoded[:])
}
