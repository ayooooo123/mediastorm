package handlers

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"strings"
)

// fmp4MaxDepth caps container nesting so hostile input cannot recurse
// without bound.
const fmp4MaxDepth = 16

// fmp4Containers lists the boxes inside moov/moof whose payload is a plain
// sequence of child boxes and is validated recursively.
var fmp4Containers = map[string]bool{
	"moov": true, "trak": true, "mdia": true, "minf": true, "stbl": true,
	"mvex": true, "edts": true, "dinf": true, "moof": true, "traf": true,
}

var (
	errFMP4Truncated = errors.New("fmp4: truncated box")
	errFMP4TooDeep   = errors.New("fmp4: boxes nested too deeply")
)

// fmp4Box is one ISO-BMFF box; data aliases the buffer it was parsed from.
type fmp4Box struct {
	typ  string
	off  int    // offset of the box within the parsed buffer
	hdr  int    // header length, including largesize and uuid usertype
	data []byte // the whole box, header included
}

func (b fmp4Box) payload() []byte { return b.data[b.hdr:] }

// readFMP4Boxes parses the sequence of boxes that exactly fills buf.
func readFMP4Boxes(buf []byte) ([]fmp4Box, error) {
	var boxes []fmp4Box
	for off := 0; off < len(buf); {
		rem := buf[off:]
		if len(rem) < 8 {
			return nil, fmt.Errorf("%w: %d trailing bytes at offset %d", errFMP4Truncated, len(rem), off)
		}
		size := uint64(binary.BigEndian.Uint32(rem))
		typ := string(rem[4:8])
		hdr := 8
		switch size {
		case 0:
			size = uint64(len(rem))
		case 1:
			if len(rem) < 16 {
				return nil, fmt.Errorf("%w: largesize header of %q at offset %d", errFMP4Truncated, typ, off)
			}
			size = binary.BigEndian.Uint64(rem[8:])
			hdr = 16
		}
		if typ == "uuid" {
			hdr += 16
		}
		if size < uint64(hdr) {
			return nil, fmt.Errorf("fmp4: box %q at offset %d has invalid size %d", typ, off, size)
		}
		if size > uint64(len(rem)) {
			return nil, fmt.Errorf("%w: box %q at offset %d claims %d bytes, %d available", errFMP4Truncated, typ, off, size, len(rem))
		}
		boxes = append(boxes, fmp4Box{typ: typ, off: off, hdr: hdr, data: rem[:size]})
		off += int(size)
	}
	return boxes, nil
}

// checkFMP4Tree validates the box structure of buf, descending into known
// container boxes.
func checkFMP4Tree(buf []byte, depth int) error {
	if depth > fmp4MaxDepth {
		return errFMP4TooDeep
	}
	boxes, err := readFMP4Boxes(buf)
	if err != nil {
		return err
	}
	for _, b := range boxes {
		if fmp4Containers[b.typ] {
			if err := checkFMP4Tree(b.payload(), depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func findFMP4Box(boxes []fmp4Box, typ string) (fmp4Box, bool) {
	for _, b := range boxes {
		if b.typ == typ {
			return b, true
		}
	}
	return fmp4Box{}, false
}

// findFMP4Path descends through container payloads along path and returns
// the box at its end.
func findFMP4Path(boxes []fmp4Box, path ...string) (fmp4Box, bool, error) {
	var cur fmp4Box
	for i, typ := range path {
		b, ok := findFMP4Box(boxes, typ)
		if !ok {
			return fmp4Box{}, false, nil
		}
		cur = b
		if i < len(path)-1 {
			var err error
			if boxes, err = readFMP4Boxes(b.payload()); err != nil {
				return fmp4Box{}, false, err
			}
		}
	}
	return cur, true, nil
}

// appendFMP4Box appends a box with the given type and payload to dst.
func appendFMP4Box(dst []byte, typ string, payload ...[]byte) []byte {
	n := 0
	for _, p := range payload {
		n += len(p)
	}
	if n+8 <= math.MaxUint32 {
		dst = binary.BigEndian.AppendUint32(dst, uint32(n+8))
		dst = append(dst, typ...)
	} else {
		dst = binary.BigEndian.AppendUint32(dst, 1)
		dst = append(dst, typ...)
		dst = binary.BigEndian.AppendUint64(dst, uint64(n+16))
	}
	for _, p := range payload {
		dst = append(dst, p...)
	}
	return dst
}

// appendFMP4Raw appends b unchanged, except that a size-0 ("to end of file")
// header is replaced by the explicit size when it fits in 32 bits.
func appendFMP4Raw(dst []byte, b fmp4Box) []byte {
	start := len(dst)
	dst = append(dst, b.data...)
	if binary.BigEndian.Uint32(b.data) == 0 && len(b.data) <= math.MaxUint32 {
		binary.BigEndian.PutUint32(dst[start:], uint32(len(b.data)))
	}
	return dst
}

// fmp4Styp is the segment type box prepended to every media segment
// (same brands as ffmpeg's movenc).
var fmp4Styp = appendFMP4Box(nil, "styp", []byte("msdh\x00\x00\x00\x00msdhmsix"))

// SplitFragmentedMP4 splits one self-contained fragmented MP4 file into an
// HLS init segment (ftyp+moov with every trak's edts box removed and all
// enclosing box sizes fixed) and a media segment (an 'styp' box followed by
// only the moof/mdat boxes — plus emsg/prft/sidx if present — in original
// order; ftyp, moov, mfra, free, skip etc. dropped).
func SplitFragmentedMP4(raw []byte) (initSeg []byte, mediaSeg []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			initSeg, mediaSeg, err = nil, nil, fmt.Errorf("fmp4: malformed input: %v", r)
		}
	}()

	top, err := readFMP4Boxes(raw)
	if err != nil {
		return nil, nil, err
	}
	var ftyp, moov *fmp4Box
	mediaSeg = append([]byte(nil), fmp4Styp...)
	moofs := 0
	for i := range top {
		b := top[i]
		switch b.typ {
		case "ftyp":
			if ftyp == nil {
				ftyp = &top[i]
			}
		case "moov":
			if moov != nil {
				return nil, nil, errors.New("fmp4: multiple moov boxes")
			}
			moov = &top[i]
		case "moof":
			if err := checkFMP4Tree(b.payload(), 1); err != nil {
				return nil, nil, err
			}
			start := len(mediaSeg)
			mediaSeg = appendFMP4Raw(mediaSeg, b)
			if err := shiftFMP4BaseDataOffsets(mediaSeg[start:], b.hdr, int64(start)-int64(b.off)); err != nil {
				return nil, nil, err
			}
			moofs++
		case "mdat", "emsg", "prft", "sidx":
			mediaSeg = appendFMP4Raw(mediaSeg, b)
		}
	}
	if ftyp == nil {
		return nil, nil, errors.New("fmp4: no ftyp box")
	}
	if moov == nil {
		return nil, nil, errors.New("fmp4: no moov box")
	}
	if moofs == 0 {
		return nil, nil, errors.New("fmp4: no moof box (not a fragmented MP4)")
	}
	if err := checkFMP4Tree(moov.payload(), 1); err != nil {
		return nil, nil, err
	}
	moovOut, err := stripFMP4Edts(*moov)
	if err != nil {
		return nil, nil, err
	}
	initSeg = appendFMP4Raw(nil, *ftyp)
	initSeg = append(initSeg, moovOut...)
	return initSeg, mediaSeg, nil
}

// stripFMP4Edts rebuilds moov without the edts box of any trak.
func stripFMP4Edts(moov fmp4Box) ([]byte, error) {
	children, err := readFMP4Boxes(moov.payload())
	if err != nil {
		return nil, err
	}
	payload := make([]byte, 0, len(moov.data))
	for _, c := range children {
		if c.typ != "trak" {
			payload = appendFMP4Raw(payload, c)
			continue
		}
		trakChildren, err := readFMP4Boxes(c.payload())
		if err != nil {
			return nil, err
		}
		trak := make([]byte, 0, len(c.data))
		for _, tc := range trakChildren {
			if tc.typ != "edts" {
				trak = appendFMP4Raw(trak, tc)
			}
		}
		payload = appendFMP4Box(payload, "trak", trak)
	}
	return appendFMP4Box(nil, "moov", payload), nil
}

// shiftFMP4BaseDataOffsets adds delta to every explicit tfhd
// base_data_offset in moof (modified in place), keeping them valid after the
// moof moved by delta bytes. Fragments using default-base-is-moof need no
// change.
func shiftFMP4BaseDataOffsets(moof []byte, hdr int, delta int64) error {
	if delta == 0 {
		return nil
	}
	children, err := readFMP4Boxes(moof[hdr:])
	if err != nil {
		return err
	}
	for _, traf := range children {
		if traf.typ != "traf" {
			continue
		}
		tfhd, ok, err := findFMP4Path([]fmp4Box{traf}, "traf", "tfhd")
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		p := tfhd.payload()
		if len(p) < 8 {
			return fmt.Errorf("%w: tfhd", errFMP4Truncated)
		}
		if binary.BigEndian.Uint32(p)&0x000001 == 0 {
			continue
		}
		if len(p) < 16 {
			return fmt.Errorf("%w: tfhd base_data_offset", errFMP4Truncated)
		}
		v := int64(binary.BigEndian.Uint64(p[8:])) + delta
		if v < 0 {
			return errors.New("fmp4: tfhd base_data_offset points before the segment")
		}
		binary.BigEndian.PutUint64(p[8:], uint64(v))
	}
	return nil
}

// fmp4SampleEntry is the first sample description of a track.
type fmp4SampleEntry struct {
	handler  string    // hdlr handler_type: "vide", "soun", ...
	format   string    // sample entry fourcc (original format for encv/enca)
	children []fmp4Box // boxes following the fixed sample entry fields
}

// fmp4SampleEntries returns the first sample entry of every video and audio
// track in the moov of buf.
func fmp4SampleEntries(buf []byte) ([]fmp4SampleEntry, error) {
	top, err := readFMP4Boxes(buf)
	if err != nil {
		return nil, err
	}
	moov, ok := findFMP4Box(top, "moov")
	if !ok {
		return nil, errors.New("fmp4: no moov box")
	}
	if err := checkFMP4Tree(moov.payload(), 1); err != nil {
		return nil, err
	}
	traks, err := readFMP4Boxes(moov.payload())
	if err != nil {
		return nil, err
	}
	var entries []fmp4SampleEntry
	for _, trak := range traks {
		if trak.typ != "trak" {
			continue
		}
		trakChildren, err := readFMP4Boxes(trak.payload())
		if err != nil {
			return nil, err
		}
		hdlr, ok, err := findFMP4Path(trakChildren, "mdia", "hdlr")
		if err != nil {
			return nil, err
		}
		if !ok || len(hdlr.payload()) < 12 {
			continue
		}
		handler := string(hdlr.payload()[8:12])
		var fixed int
		switch handler {
		case "vide":
			fixed = 78
		case "soun":
			fixed = 28
		default:
			continue
		}
		stsd, ok, err := findFMP4Path(trakChildren, "mdia", "minf", "stbl", "stsd")
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if len(stsd.payload()) < 8 {
			return nil, fmt.Errorf("%w: stsd", errFMP4Truncated)
		}
		descs, err := readFMP4Boxes(stsd.payload()[8:])
		if err != nil {
			return nil, err
		}
		if len(descs) == 0 {
			continue
		}
		desc := descs[0]
		p := desc.payload()
		if handler == "soun" && len(p) >= 10 {
			switch binary.BigEndian.Uint16(p[8:]) {
			case 1:
				fixed += 16
			case 2:
				fixed += 36
			}
		}
		if len(p) < fixed {
			return nil, fmt.Errorf("%w: %q sample entry", errFMP4Truncated, desc.typ)
		}
		children, err := readFMP4Boxes(p[fixed:])
		if err != nil {
			return nil, err
		}
		format := desc.typ
		if format == "encv" || format == "enca" {
			if frma, ok, err := findFMP4Path(children, "sinf", "frma"); err != nil {
				return nil, err
			} else if ok && len(frma.payload()) >= 4 {
				format = string(frma.payload()[:4])
			}
		}
		entries = append(entries, fmp4SampleEntry{handler: handler, format: format, children: children})
	}
	return entries, nil
}

func firstFMP4Entry(entries []fmp4SampleEntry, handler string) (fmp4SampleEntry, bool) {
	for _, e := range entries {
		if e.handler == handler {
			return e, true
		}
	}
	return fmp4SampleEntry{}, false
}

// HLSCodecString derives the RFC 6381 codec strings of the first video and
// the first audio track of an init segment (or full MP4). A missing track
// yields an empty string; an unsupported or unparsable codec yields an error,
// with any string that could be derived still returned.
func HLSCodecString(init []byte) (video string, audio string, err error) {
	defer func() {
		if r := recover(); r != nil {
			video, audio, err = "", "", fmt.Errorf("fmp4: malformed input: %v", r)
		}
	}()

	entries, err := fmp4SampleEntries(init)
	if err != nil {
		return "", "", err
	}
	var errs []error
	v, hasVideo := firstFMP4Entry(entries, "vide")
	if hasVideo {
		if video, err = fmp4VideoCodec(v); err != nil {
			errs = append(errs, err)
		}
	}
	a, hasAudio := firstFMP4Entry(entries, "soun")
	if hasAudio {
		if audio, err = fmp4AudioCodec(a); err != nil {
			errs = append(errs, err)
		}
	}
	if !hasVideo && !hasAudio {
		errs = append(errs, errors.New("fmp4: no video or audio track"))
	}
	return video, audio, errors.Join(errs...)
}

func fmp4VideoCodec(e fmp4SampleEntry) (string, error) {
	switch e.format {
	case "avc1", "avc3", "dva1", "dvav":
		c, ok := findFMP4Box(e.children, "avcC")
		if !ok || len(c.payload()) < 4 {
			return "", fmt.Errorf("fmp4: %q sample entry without valid avcC", e.format)
		}
		prefix := e.format
		switch prefix {
		case "dva1":
			prefix = "avc1"
		case "dvav":
			prefix = "avc3"
		}
		p := c.payload()
		return fmt.Sprintf("%s.%02x%02x%02x", prefix, p[1], p[2], p[3]), nil
	case "hvc1", "hev1", "dvh1", "dvhe":
		c, ok := findFMP4Box(e.children, "hvcC")
		if !ok || len(c.payload()) < 13 {
			return "", fmt.Errorf("fmp4: %q sample entry without valid hvcC", e.format)
		}
		prefix := e.format
		if prefix == "dvh1" || prefix == "dvhe" {
			prefix = "hvc1"
		}
		return hevcCodecString(prefix, c.payload()), nil
	case "av01", "dav1":
		c, ok := findFMP4Box(e.children, "av1C")
		if !ok || len(c.payload()) < 3 {
			return "", fmt.Errorf("fmp4: %q sample entry without valid av1C", e.format)
		}
		return av1CodecString(c.payload()), nil
	}
	return "", fmt.Errorf("fmp4: unsupported video sample entry %q", e.format)
}

// hevcCodecString formats an hvcC record per ISO/IEC 14496-15 Annex E.
func hevcCodecString(prefix string, p []byte) string {
	var sb strings.Builder
	sb.WriteString(prefix)
	sb.WriteByte('.')
	if space := p[1] >> 6; space > 0 {
		sb.WriteByte('A' + space - 1)
	}
	fmt.Fprintf(&sb, "%d", p[1]&0x1f)
	fmt.Fprintf(&sb, ".%X", bits.Reverse32(binary.BigEndian.Uint32(p[2:6])))
	tier := byte('L')
	if p[1]&0x20 != 0 {
		tier = 'H'
	}
	fmt.Fprintf(&sb, ".%c%d", tier, p[12])
	constraints := p[6:12]
	n := len(constraints)
	for n > 0 && constraints[n-1] == 0 {
		n--
	}
	for _, c := range constraints[:n] {
		fmt.Fprintf(&sb, ".%X", c)
	}
	return sb.String()
}

// av1CodecString formats an av1C record as "av01.P.LLT.DD".
func av1CodecString(p []byte) string {
	profile := p[1] >> 5
	level := p[1] & 0x1f
	tier := 'M'
	if p[2]&0x80 != 0 {
		tier = 'H'
	}
	depth := 8
	if p[2]&0x40 != 0 {
		depth = 10
		if p[2]&0x20 != 0 {
			depth = 12
		}
	}
	return fmt.Sprintf("av01.%d.%02d%c.%02d", profile, level, tier, depth)
}

func fmp4AudioCodec(e fmp4SampleEntry) (string, error) {
	switch e.format {
	case "mp4a":
		esds, ok := findFMP4Box(e.children, "esds")
		if !ok {
			return "mp4a.40.2", nil
		}
		return mp4aCodecString(esds.payload())
	case ".mp3":
		return "mp4a.6B", nil
	case "ac-3", "ec-3", "fLaC":
		return e.format, nil
	case "Opus":
		return "opus", nil
	}
	return "", fmt.Errorf("fmp4: unsupported audio sample entry %q", e.format)
}

// mp4aCodecString reads the object type (and AAC audio object type) from an
// esds payload.
func mp4aCodecString(esds []byte) (string, error) {
	if len(esds) < 4 {
		return "", fmt.Errorf("%w: esds", errFMP4Truncated)
	}
	tag, es, _, err := readMP4Descriptor(esds[4:])
	if err != nil {
		return "", err
	}
	if tag != 0x03 || len(es) < 3 {
		return "", errors.New("fmp4: esds without ES_Descriptor")
	}
	flags := es[2]
	es = es[3:]
	if flags&0x80 != 0 { // streamDependenceFlag
		es = es[min(2, len(es)):]
	}
	if flags&0x40 != 0 && len(es) > 0 { // URL_Flag
		es = es[min(1+int(es[0]), len(es)):]
	}
	if flags&0x20 != 0 { // OCRstreamFlag
		es = es[min(2, len(es)):]
	}
	for len(es) > 0 {
		tag, dc, rest, err := readMP4Descriptor(es)
		if err != nil {
			return "", err
		}
		es = rest
		if tag != 0x04 {
			continue
		}
		if len(dc) < 13 {
			return "", fmt.Errorf("%w: DecoderConfigDescriptor", errFMP4Truncated)
		}
		oti := dc[0]
		if oti != 0x40 {
			return fmt.Sprintf("mp4a.%X", oti), nil
		}
		for sub := dc[13:]; len(sub) > 0; {
			tag, dsi, rest, err := readMP4Descriptor(sub)
			if err != nil {
				return "", err
			}
			sub = rest
			if tag != 0x05 || len(dsi) == 0 {
				continue
			}
			aot := int(dsi[0] >> 3)
			if aot == 31 {
				if len(dsi) < 2 {
					return "", fmt.Errorf("%w: AudioSpecificConfig", errFMP4Truncated)
				}
				aot = 32 + int(dsi[0]&0x07)<<3 + int(dsi[1]>>5)
			}
			return fmt.Sprintf("mp4a.40.%d", aot), nil
		}
		return "mp4a.40.2", nil
	}
	return "", errors.New("fmp4: esds without DecoderConfigDescriptor")
}

// readMP4Descriptor reads one MPEG-4 descriptor (tag, expandable size) and
// returns its body and the bytes following it.
func readMP4Descriptor(b []byte) (tag byte, body, rest []byte, err error) {
	if len(b) < 2 {
		return 0, nil, nil, fmt.Errorf("%w: descriptor", errFMP4Truncated)
	}
	tag = b[0]
	size, i := 0, 1
	for ; i <= 4; i++ {
		if i >= len(b) {
			return 0, nil, nil, fmt.Errorf("%w: descriptor size", errFMP4Truncated)
		}
		size = size<<7 | int(b[i]&0x7f)
		if b[i]&0x80 == 0 {
			break
		}
	}
	if i > 4 {
		return 0, nil, nil, errors.New("fmp4: descriptor size too long")
	}
	b = b[i+1:]
	if size > len(b) {
		return 0, nil, nil, fmt.Errorf("%w: descriptor 0x%02x claims %d bytes, %d available", errFMP4Truncated, tag, size, len(b))
	}
	return tag, b[:size], b[size:], nil
}

// DolbyVisionCodecString returns "dvh1.PP.LL" (e.g. "dvh1.08.06") from a
// dvcC/dvvC box of the first video track, plus the
// bl_signal_compatibility_id; ok=false when no DV config is present. The
// prefix follows the base codec: dvh1/dvhe for HEVC, dav1 for AV1,
// dva1/dvav for AVC.
func DolbyVisionCodecString(init []byte) (codec string, compatibilityID int, ok bool) {
	defer func() {
		if recover() != nil {
			codec, compatibilityID, ok = "", 0, false
		}
	}()

	entries, err := fmp4SampleEntries(init)
	if err != nil {
		return "", 0, false
	}
	v, found := firstFMP4Entry(entries, "vide")
	if !found {
		return "", 0, false
	}
	var cfg fmp4Box
	for _, typ := range []string{"dvcC", "dvvC", "dvwC"} {
		if cfg, found = findFMP4Box(v.children, typ); found {
			break
		}
	}
	if !found || len(cfg.payload()) < 5 {
		return "", 0, false
	}
	p := cfg.payload()
	profile := int(p[2] >> 1)
	level := int(p[2]&0x01)<<5 | int(p[3]>>3)
	compatibilityID = int(p[4] >> 4)

	prefix := "dvh1"
	switch v.format {
	case "dvh1", "dvhe", "dav1", "dva1", "dvav":
		prefix = v.format
	case "hev1":
		prefix = "dvhe"
	case "av01":
		prefix = "dav1"
	case "avc1":
		prefix = "dva1"
	case "avc3":
		prefix = "dvav"
	}
	return fmt.Sprintf("%s.%02d.%02d", prefix, profile, level), compatibilityID, true
}
