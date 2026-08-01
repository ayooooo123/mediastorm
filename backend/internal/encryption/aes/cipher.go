package aes

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"

	"novastream/internal/encryption"
	"novastream/internal/nzb/utils"
)

const BlockSize = 16

type Cipher struct{}

func New() *Cipher { return &Cipher{} }

func encryptedSize(fileSize int64) int64 {
	if fileSize%BlockSize == 0 {
		return fileSize
	}
	return fileSize + BlockSize - fileSize%BlockSize
}

func (c *Cipher) Name() encryption.CipherType { return encryption.CipherType("aes") }

func (c *Cipher) OverheadSize(fileSize int64) int64 {
	return c.EncryptedSize(fileSize) - fileSize
}

func (c *Cipher) EncryptedSize(fileSize int64) int64 { return encryptedSize(fileSize) }

func (c *Cipher) DecryptedSize(encryptedFileSize int64) (int64, error) {
	return max(encryptedFileSize-BlockSize, 0), nil
}

// Open decrypts an AES-CBC archive payload. key and IV are base64-encoded in the
// legacy metadata password and salt fields; they are derived by rardecode at
// import time, so the original archive password is never persisted.
func (c *Cipher) Open(
	ctx context.Context,
	rh *utils.RangeHeader,
	decryptedFileSize int64,
	key string,
	iv string,
	getReader func(context.Context, int64, int64) (io.ReadCloser, error),
) (io.ReadCloser, error) {
	if key == "" {
		return nil, fmt.Errorf("AES key is required")
	}
	if iv == "" {
		return nil, fmt.Errorf("AES IV is required")
	}

	requestEnd := int64(-1)
	if rh != nil {
		requestEnd = rh.End
	}
	decodedKey, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return nil, fmt.Errorf("decode AES key: %w", err)
	}
	decodedIV, err := base64.StdEncoding.DecodeString(iv)
	if err != nil {
		return nil, fmt.Errorf("decode AES IV: %w", err)
	}
	reader, err := newDecryptReader(ctx, getReader, decodedKey, decodedIV, decryptedFileSize, c.EncryptedSize(decryptedFileSize), requestEnd)
	if err != nil {
		return nil, fmt.Errorf("create AES decrypt reader: %w", err)
	}
	if rh != nil && rh.Start > 0 {
		if _, err := reader.Seek(rh.Start, io.SeekStart); err != nil {
			_ = reader.Close()
			return nil, fmt.Errorf("seek AES decrypt reader: %w", err)
		}
	}
	return reader, nil
}
