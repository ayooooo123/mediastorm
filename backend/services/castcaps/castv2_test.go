package castcaps

import (
	"encoding/binary"
	"testing"
)

func TestCastMessage_EncodeDecodeRoundTrip(t *testing.T) {
	tests := []castMessage{
		{
			SourceID:      "sender-1",
			DestinationID: "receiver-1",
			Namespace:     "urn:x-cast:com.example.namespace",
			Payload:       `{"type": "TEST"}`,
		},
		{
			SourceID:      "",
			DestinationID: "",
			Namespace:     "",
			Payload:       "",
		},
		{
			SourceID:      "sender",
			DestinationID: "receiver",
			Namespace:     "test",
			Payload:       "UTF-8 payload: ✓ 🚀",
		},
	}

	for i, tc := range tests {
		encoded := tc.encode()

		// Verify the 4-byte length prefix matches the payload length
		length := binary.BigEndian.Uint32(encoded[:4])
		if length != uint32(len(encoded)-4) {
			t.Errorf("test %d: expected length prefix %d, got %d", i, len(encoded)-4, length)
		}

		decoded, err := decodeCastMessage(encoded[4:])
		if err != nil {
			t.Errorf("test %d: decode failed: %v", i, err)
			continue
		}

		if decoded.SourceID != tc.SourceID ||
			decoded.DestinationID != tc.DestinationID ||
			decoded.Namespace != tc.Namespace ||
			decoded.Payload != tc.Payload {
			t.Errorf("test %d: round trip failed.\nWant: %+v\nGot:  %+v", i, tc, decoded)
		}
	}
}

func TestDecodeCastMessage_Errors(t *testing.T) {
	t.Run("truncated varint", func(t *testing.T) {
		_, err := decodeCastMessage([]byte{0x80}) // Malformed varint
		if err == nil {
			t.Error("expected error on truncated varint")
		}
	})

	t.Run("truncated length-delimited", func(t *testing.T) {
		// Field 2 (SourceID), length 10, but only 1 byte follows
		body := appendVarintField(nil, 2, 10)
		body = append(body, 'x')
		_, err := decodeCastMessage(body)
		if err == nil {
			t.Error("expected error on truncated length-delimited field")
		}
	})

	t.Run("unsupported wire type", func(t *testing.T) {
		// Field 1, wire type 1 (64-bit), which is not handled
		body := []byte{(1 << 3) | 1, 0, 0, 0, 0, 0, 0, 0, 0}
		_, err := decodeCastMessage(body)
		if err == nil || err.Error() != "unsupported wire type in field 1" {
			t.Errorf("expected unsupported wire type error, got %v", err)
		}
	})
}
