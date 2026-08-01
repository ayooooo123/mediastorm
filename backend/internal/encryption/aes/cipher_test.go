package aes

import (
	"bytes"
	"context"
	stdaes "crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"io"
	"testing"

	"novastream/internal/nzb/utils"
)

func TestCipherDecryptsFullAndSeekedRanges(t *testing.T) {
	plain := bytes.Repeat([]byte("seekable-rar-payload-"), 257)
	key := []byte("0123456789abcdef0123456789abcdef")
	iv := []byte("0123456789abcdef")
	padded := make([]byte, encryptedSize(int64(len(plain))))
	copy(padded, plain)
	block, err := stdaes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(padded, padded)

	getReader := func(_ context.Context, start, end int64) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(padded[start : end+1])), nil
	}

	for _, test := range []struct {
		name       string
		start, end int64
	}{
		{name: "full", start: 0, end: int64(len(plain) - 1)},
		{name: "unaligned middle", start: 137, end: 1789},
		{name: "tail", start: int64(len(plain) - 91), end: int64(len(plain) - 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader, err := New().Open(context.Background(), &utils.RangeHeader{Start: test.start, End: test.end}, int64(len(plain)), base64.StdEncoding.EncodeToString(key), base64.StdEncoding.EncodeToString(iv), getReader)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			got, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			want := plain[test.start : test.end+1]
			if !bytes.Equal(got, want) {
				t.Fatalf("decrypted range mismatch: got %d bytes, want %d", len(got), len(want))
			}
		})
	}
}
