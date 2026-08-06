package handlers

import (
	"encoding/binary"
	"net/http"
	"testing"

	"novastream/services/streaming"
)

func buildMP4Box(typ string, payload []byte) []byte {
	size := uint32(8 + len(payload))
	box := make([]byte, size)
	binary.BigEndian.PutUint32(box[0:4], size)
	copy(box[4:8], []byte(typ))
	copy(box[8:], payload)
	return box
}

func TestMp4MoovAtEndOffset(t *testing.T) {
	ftyp := buildMP4Box("ftyp", []byte("isom"))
	// mdat claims to span almost the whole file; moov lives after it.
	mdatHeader := make([]byte, 8)
	binary.BigEndian.PutUint32(mdatHeader[0:4], 1000) // size includes header
	copy(mdatHeader[4:8], []byte("mdat"))
	prefix := append(append([]byte{}, ftyp...), mdatHeader...)

	const totalSize int64 = 2000
	off, ok := mp4MoovAtEndOffset(prefix, totalSize)
	if !ok {
		t.Fatalf("expected moov-at-end detection")
	}
	want := int64(len(ftyp)) + 1000
	if off != want {
		t.Fatalf("moov offset = %d, want %d", off, want)
	}

	// moov already at front
	moov := buildMP4Box("moov", []byte{0})
	front := append(append([]byte{}, ftyp...), moov...)
	if _, ok := mp4MoovAtEndOffset(front, totalSize); ok {
		t.Fatalf("did not expect moov-at-end when moov is early")
	}
}

func TestMp4MoovAtEndOffset64BitMdat(t *testing.T) {
	ftyp := buildMP4Box("ftyp", []byte("isom"))
	// largesize form: size=1, type=mdat, 8-byte size
	hdr := make([]byte, 16)
	binary.BigEndian.PutUint32(hdr[0:4], 1)
	copy(hdr[4:8], []byte("mdat"))
	const mdatSize int64 = 5_791_599_032
	binary.BigEndian.PutUint64(hdr[8:16], uint64(mdatSize))
	prefix := append(append([]byte{}, ftyp...), hdr...)

	total := int64(len(ftyp)) + mdatSize + 1024
	off, ok := mp4MoovAtEndOffset(prefix, total)
	if !ok {
		t.Fatalf("expected 64-bit mdat moov-at-end")
	}
	want := int64(len(ftyp)) + mdatSize
	if off != want {
		t.Fatalf("moov offset = %d, want %d", off, want)
	}
}

func TestExtractMP4Box(t *testing.T) {
	ftyp := buildMP4Box("ftyp", []byte("isom\x00\x00\x02\x00isom"))
	moov := buildMP4Box("moov", []byte{1, 2, 3})
	blob := append(append([]byte{}, ftyp...), moov...)
	got := extractMP4Box(blob, "moov")
	if len(got) != len(moov) {
		t.Fatalf("moov box len = %d, want %d", len(got), len(moov))
	}
	if string(got[4:8]) != "moov" {
		t.Fatalf("type = %q", got[4:8])
	}
}

func TestProviderResponseTotalSize(t *testing.T) {
	headers := make(http.Header)
	headers.Set("Content-Range", "bytes 0-1023/5801609048")
	resp := &streaming.Response{
		Status:  http.StatusPartialContent,
		Headers: headers,
	}
	if got := providerResponseTotalSize(resp); got != 5801609048 {
		t.Fatalf("total from Content-Range = %d, want 5801609048", got)
	}

	full := &streaming.Response{
		Status:        http.StatusOK,
		ContentLength: 42,
	}
	if got := providerResponseTotalSize(full); got != 42 {
		t.Fatalf("total from 200 ContentLength = %d, want 42", got)
	}
}
