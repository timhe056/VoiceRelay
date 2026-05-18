package header

import (
	"crypto/rand"
	"testing"
)

func TestParseValid(t *testing.T) {
	var token [16]byte
	_, _ = rand.Read(token[:])

	buf := make([]byte, Size)
	p := Packet{Token: token, Sequence: 42, Channel: 1, Codec: CodecOpus}
	p.Write(buf)

	got, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if got.Token != token {
		t.Error("token mismatch")
	}
	if got.Sequence != 42 {
		t.Errorf("sequence = %d, want 42", got.Sequence)
	}
	if got.Channel != 1 {
		t.Errorf("channel = %d, want 1", got.Channel)
	}
	if got.Codec != CodecOpus {
		t.Errorf("codec = %d, want %d", got.Codec, CodecOpus)
	}
}

func TestParseTooShort(t *testing.T) {
	_, err := Parse(make([]byte, 10))
	if err != ErrTooShort {
		t.Errorf("expected ErrTooShort, got %v", err)
	}
}

func TestRoundTrip(t *testing.T) {
	var token [16]byte
	_, _ = rand.Read(token[:])

	for _, seq := range []uint32{0, 1, 0xFFFFFFFF} {
		for _, ch := range []byte{0, 1, 2, 3, 255} {
			for _, codec := range []CodecType{CodecOpus, CodecADPCM, CodecMuLaw} {
				orig := Packet{Token: token, Sequence: seq, Channel: ch, Codec: codec}
				buf := make([]byte, Size)
				orig.Write(buf)
				got, err := Parse(buf)
				if err != nil {
					t.Fatalf("roundtrip failed: %v", err)
				}
				if got != orig {
					t.Errorf("roundtrip mismatch: %+v != %+v", got, orig)
				}
			}
		}
	}
}

func TestBuildPacket(t *testing.T) {
	var token [16]byte
	_, _ = rand.Read(token[:])
	payload := []byte{0x01, 0x02, 0x03}

	pkt := BuildPacket(token, 100, 2, CodecMuLaw, payload)

	if len(pkt) != Size+len(payload) {
		t.Fatalf("len = %d, want %d", len(pkt), Size+len(payload))
	}

	hdr, err := Parse(pkt)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if hdr.Token != token || hdr.Sequence != 100 || hdr.Channel != 2 || hdr.Codec != CodecMuLaw {
		t.Error("header mismatch")
	}
	if string(pkt[Size:]) != string(payload) {
		t.Error("payload mismatch")
	}
}

func TestTokenFromHex(t *testing.T) {
	hexStr := "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6"
	token, err := TokenFromHex(hexStr)
	if err != nil {
		t.Fatalf("TokenFromHex failed: %v", err)
	}
	if len(token) != 16 {
		t.Errorf("token len = %d, want 16", len(token))
	}

	// Invalid hex
	_, err = TokenFromHex("zzzz")
	if err == nil {
		t.Error("expected error for invalid hex")
	}

	// Wrong length (too short)
	_, err = TokenFromHex("a1b2")
	if err == nil {
		t.Error("expected error for short token")
	}
}
