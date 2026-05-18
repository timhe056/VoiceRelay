// Package header defines the 22-byte custom voice packet header used by VoiceRelay.
// The header is prepended to every Opus voice frame sent over UDP.
package header

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
)

// Size is the fixed header length in bytes.
const Size = 22

// CodecType identifies the voice encoding used in the payload.
type CodecType byte

const (
	CodecOpus  CodecType = 0
	CodecADPCM CodecType = 1
	CodecMuLaw CodecType = 2
)

// Packet represents a parsed voice packet header.
// All fields are copied out of the wire format; the original buffer is not retained.
type Packet struct {
	Token    [16]byte
	Sequence uint32
	Channel  byte
	Codec    CodecType
}

var (
	ErrTooShort = errors.New("packet too short for header")
)

// Parse reads a 22-byte header from the given slice.
// The caller must ensure data is at least Size bytes long.
func Parse(data []byte) (Packet, error) {
	if len(data) < Size {
		return Packet{}, ErrTooShort
	}
	var p Packet
	copy(p.Token[:], data[0:16])
	p.Sequence = binary.BigEndian.Uint32(data[16:20])
	p.Channel = data[20]
	p.Codec = CodecType(data[21])
	return p, nil
}

// Write encodes the header into buf, which must be at least Size bytes.
func (p Packet) Write(buf []byte) {
	copy(buf[0:16], p.Token[:])
	binary.BigEndian.PutUint32(buf[16:20], p.Sequence)
	buf[20] = p.Channel
	buf[21] = byte(p.Codec)
}

// BuildPacket creates a complete voice packet (header + payload) in a single allocation.
func BuildPacket(token [16]byte, seq uint32, channel byte, codec CodecType, payload []byte) []byte {
	pkt := make([]byte, Size+len(payload))
	p := Packet{Token: token, Sequence: seq, Channel: channel, Codec: codec}
	p.Write(pkt)
	copy(pkt[Size:], payload)
	return pkt
}

// TokenFromHex parses a hex-encoded token string (32 hex chars) into [16]byte.
func TokenFromHex(s string) ([16]byte, error) {
	var token [16]byte
	b, err := hex.DecodeString(s)
	if err != nil {
		return token, err
	}
	if len(b) != 16 {
		return token, errors.New("token must be 16 bytes (32 hex chars)")
	}
	copy(token[:], b)
	return token, nil
}
