package aes

import (
	"context"
	stdaes "crypto/aes"
	"crypto/cipher"
	"fmt"
	"io"
)

type decryptReader struct {
	ctx           context.Context
	getReader     func(context.Context, int64, int64) (io.ReadCloser, error)
	source        io.ReadCloser
	key           []byte
	originalIV    []byte
	decrypter     cipher.BlockMode
	buffer        []byte
	bufferPos     int
	bufferLen     int
	offset        int64
	size          int64
	encryptedSize int64
	requestEnd    int64
	closed        bool
}

func newDecryptReader(
	ctx context.Context,
	getReader func(context.Context, int64, int64) (io.ReadCloser, error),
	key, iv []byte,
	decryptedSize, encryptedSize, requestEnd int64,
) (*decryptReader, error) {
	if len(key) != 16 && len(key) != 24 && len(key) != 32 {
		return nil, fmt.Errorf("invalid AES key size: %d", len(key))
	}
	if len(iv) != stdaes.BlockSize {
		return nil, fmt.Errorf("invalid AES IV size: %d", len(iv))
	}
	block, err := stdaes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	ivCopy := append([]byte(nil), iv...)
	return &decryptReader{
		ctx:           ctx,
		getReader:     getReader,
		key:           append([]byte(nil), key...),
		originalIV:    append([]byte(nil), iv...),
		decrypter:     cipher.NewCBCDecrypter(block, ivCopy),
		buffer:        make([]byte, stdaes.BlockSize*64),
		size:          decryptedSize,
		encryptedSize: encryptedSize,
		requestEnd:    requestEnd,
	}, nil
}

func (r *decryptReader) sourceEnd() int64 {
	end := r.encryptedSize - 1
	if r.requestEnd >= 0 {
		bounded := encryptedSize(r.requestEnd+1) - 1
		if bounded < end {
			end = bounded
		}
	}
	return end
}

func (r *decryptReader) Read(p []byte) (int, error) {
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	if r.source == nil {
		var err error
		r.source, err = r.getReader(r.ctx, 0, r.sourceEnd())
		if err != nil {
			return 0, fmt.Errorf("initialize AES source: %w", err)
		}
	}

	effectiveSize := r.size
	if r.requestEnd >= 0 && r.requestEnd+1 < effectiveSize {
		effectiveSize = r.requestEnd + 1
	}
	total := 0
	for total < len(p) {
		if r.bufferPos < r.bufferLen {
			n := copy(p[total:], r.buffer[r.bufferPos:r.bufferLen])
			r.bufferPos += n
			r.offset += int64(n)
			total += n
			continue
		}

		readSize := len(r.buffer)
		if remaining := effectiveSize - r.offset; int64(readSize) > remaining {
			readSize = int(remaining)
			if readSize%stdaes.BlockSize != 0 {
				readSize += stdaes.BlockSize - readSize%stdaes.BlockSize
			}
		}
		if readSize <= 0 {
			if total > 0 {
				return total, nil
			}
			return 0, io.EOF
		}

		encrypted := make([]byte, readSize)
		n, err := io.ReadFull(r.source, encrypted)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return total, err
		}
		n -= n % stdaes.BlockSize
		if n > 0 {
			r.decrypter.CryptBlocks(encrypted[:n], encrypted[:n])
			plainLen := n
			if r.offset+int64(plainLen) > effectiveSize {
				plainLen = int(effectiveSize - r.offset)
			}
			copy(r.buffer, encrypted[:plainLen])
			r.bufferLen = plainLen
			r.bufferPos = 0
		}
		if (err == io.EOF || err == io.ErrUnexpectedEOF) && r.bufferLen == 0 {
			if total > 0 {
				return total, nil
			}
			return 0, io.EOF
		}
	}
	return total, nil
}

func (r *decryptReader) Seek(offset int64, whence int) (int64, error) {
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	var absolute int64
	switch whence {
	case io.SeekStart:
		absolute = offset
	case io.SeekCurrent:
		absolute = r.offset + offset
	case io.SeekEnd:
		absolute = r.size + offset
	default:
		return 0, fmt.Errorf("invalid whence: %d", whence)
	}
	if absolute < 0 || absolute > r.size {
		return 0, fmt.Errorf("invalid seek position: %d", absolute)
	}
	if absolute == r.offset {
		return absolute, nil
	}
	if r.source != nil {
		_ = r.source.Close()
		r.source = nil
	}

	blockNumber := absolute / stdaes.BlockSize
	blockOffset := absolute % stdaes.BlockSize
	sourceOffset := int64(0)
	newIV := append([]byte(nil), r.originalIV...)
	if blockNumber > 0 {
		sourceOffset = (blockNumber - 1) * stdaes.BlockSize
	}
	newSource, err := r.getReader(r.ctx, sourceOffset, r.sourceEnd())
	if err != nil {
		return 0, fmt.Errorf("open AES seek source: %w", err)
	}
	if blockNumber > 0 {
		newIV = make([]byte, stdaes.BlockSize)
		if _, err := io.ReadFull(newSource, newIV); err != nil {
			_ = newSource.Close()
			return 0, fmt.Errorf("read AES seek IV: %w", err)
		}
	}
	block, err := stdaes.NewCipher(r.key)
	if err != nil {
		_ = newSource.Close()
		return 0, err
	}
	r.source = newSource
	r.decrypter = cipher.NewCBCDecrypter(block, newIV)
	r.offset = blockNumber * stdaes.BlockSize
	r.bufferPos, r.bufferLen = 0, 0
	if blockOffset > 0 {
		if _, err := io.ReadFull(r, make([]byte, blockOffset)); err != nil {
			return 0, fmt.Errorf("skip within AES block: %w", err)
		}
	}
	return absolute, nil
}

func (r *decryptReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	if r.source != nil {
		return r.source.Close()
	}
	return nil
}
