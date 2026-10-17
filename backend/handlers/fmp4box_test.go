package handlers

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func box(typ string, payload ...[]byte) []byte { return appendFMP4Box(nil, typ, payload...) }

func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }

func u64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }

func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

// largeBox builds a box using the 64-bit largesize header form.
func largeBox(typ string, payload ...[]byte) []byte {
	p := cat(payload...)
	return cat(u32(1), []byte(typ), u64(uint64(16+len(p))), p)
}

func visualEntry(fourcc string, children ...[]byte) []byte {
	return box(fourcc, append([][]byte{make([]byte, 78)}, children...)...)
}

func audioEntry(fourcc string, children ...[]byte) []byte {
	return box(fourcc, append([][]byte{make([]byte, 28)}, children...)...)
}

// testTrak builds a minimal trak; elst==nil omits the edts box.
func testTrak(id uint32, handler string, entry, elst []byte) []byte {
	hdlr := box("hdlr", make([]byte, 8), []byte(handler), make([]byte, 13))
	stbl := box("stbl", box("stsd", u32(0), u32(1), entry), box("stts", u32(0), u32(0)))
	mdia := box("mdia", box("mdhd", make([]byte, 24)), hdlr, box("minf", box("dinf", box("dref", u32(0), u32(0))), stbl))
	parts := [][]byte{box("tkhd", u32(0), u32(0), u32(0), u32(id), make([]byte, 68))}
	if elst != nil {
		parts = append(parts, box("edts", box("elst", elst)))
	}
	return box("trak", append(parts, mdia)...)
}

func testMoof(seq uint32, tfhdFlags uint32, extra ...[]byte) []byte {
	tfhd := box("tfhd", u32(tfhdFlags), u32(1), cat(extra...))
	return box("moof", box("mfhd", u32(0), u32(seq)), box("traf", tfhd, box("tfdt", u32(0x01000000), u64(uint64(seq)*90000)), box("trun", u32(0), u32(0))))
}

var (
	testFtyp     = box("ftyp", []byte("iso5"), u32(512), []byte("iso5iso6mp41"))
	testAvcEntry = visualEntry("avc1", box("avcC", []byte{1, 0x64, 0x00, 0x1f, 0xff, 0xe0, 0x00}))
	testAacEntry = audioEntry("mp4a", box("esds", u32(0), testESDescriptor(0x40, []byte{0x12, 0x10})))
)

// testMoov builds a moov with an avc1 video and an AAC audio track.
func testMoov(videoElst, audioElst []byte) []byte {
	return box("moov",
		box("mvhd", make([]byte, 100)),
		testTrak(1, "vide", testAvcEntry, videoElst),
		testTrak(2, "soun", testAacEntry, audioElst),
		box("mvex", box("trex", make([]byte, 24)), box("trex", make([]byte, 24))),
	)
}

func testESDescriptor(oti byte, asc []byte) []byte {
	dc := cat([]byte{oti, 0x15, 0, 0, 0}, u32(128000), u32(128000))
	if asc != nil {
		dc = cat(dc, []byte{0x05, byte(len(asc))}, asc)
	}
	es := cat([]byte{0x00, 0x01, 0x00, 0x04, 0x80, 0x80, 0x80, byte(len(dc))}, dc, []byte{0x06, 0x01, 0x02})
	return cat([]byte{0x03, 0x80, 0x80, 0x80, byte(len(es))}, es)
}

func elst(mediaTime uint32) []byte {
	return cat(u32(0), u32(1), u32(0), u32(mediaTime), u32(0x00010000))
}

func topBoxTypes(t *testing.T, b []byte) []string {
	t.Helper()
	boxes, err := readFMP4Boxes(b)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	var types []string
	for _, bx := range boxes {
		types = append(types, bx.typ)
	}
	return types
}

func TestSplitFMP4OrderAndEdts(t *testing.T) {
	moof1, mdat1 := testMoof(1, 0x020000), box("mdat", []byte("frame-one"))
	moof2, mdat2 := testMoof(2, 0x020000), box("mdat", []byte("frame-two!"))
	emsg := box("emsg", make([]byte, 12))
	raw := cat(testFtyp, testMoov(elst(3003), elst(1024)), moof1, mdat1, box("free", []byte("xx")), emsg, moof2, mdat2,
		box("mfra", box("tfra", make([]byte, 16)), box("mfro", u32(0), u32(8))))

	initSeg, media, err := SplitFragmentedMP4(raw)
	if err != nil {
		t.Fatalf("SplitFragmentedMP4: %v", err)
	}

	if want := cat(fmp4Styp, moof1, mdat1, emsg, moof2, mdat2); !bytes.Equal(media, want) {
		t.Fatalf("media segment mismatch: got types %v", topBoxTypes(t, media))
	}
	if got := topBoxTypes(t, media); got[0] != "styp" {
		t.Fatalf("media starts with %q, want styp", got[0])
	}

	if err := checkFMP4Tree(initSeg, 0); err != nil {
		t.Fatalf("init does not re-parse: %v", err)
	}
	if got := strings.Join(topBoxTypes(t, initSeg), ","); got != "ftyp,moov" {
		t.Fatalf("init boxes = %s, want ftyp,moov", got)
	}
	if bytes.Contains(initSeg, []byte("edts")) || bytes.Contains(initSeg, []byte("elst")) {
		t.Fatal("init still contains edts/elst")
	}
	moov := initSeg[len(testFtyp):]
	if size := binary.BigEndian.Uint32(moov); int(size) != len(moov) {
		t.Fatalf("moov size field %d, box length %d", size, len(moov))
	}
	if want := cat(testFtyp, testMoov(nil, nil)); !bytes.Equal(initSeg, want) {
		t.Fatal("init differs from the same moov built without edts")
	}
}

func TestSplitFMP4InitIdenticalAcrossElst(t *testing.T) {
	seg := func(v, a []byte, seq uint32) []byte {
		return cat(testFtyp, testMoov(v, a), testMoof(seq, 0x020000), box("mdat", []byte{byte(seq)}))
	}
	init0, _, err := SplitFragmentedMP4(seg(elst(3003), elst(1024), 1))
	if err != nil {
		t.Fatal(err)
	}
	init1, _, err := SplitFragmentedMP4(seg(elst(0), nil, 7))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(init0, init1) {
		t.Fatal("init segments differ although inputs differ only in edts")
	}
}

func TestSplitFMP4LargesizeAndSizeZero(t *testing.T) {
	moof := largeBox("moof", box("mfhd", u32(0), u32(1)), box("traf", box("tfhd", u32(0x020000), u32(1))))
	mdat := largeBox("mdat", []byte("payload"))
	tail := box("mdat", []byte("to-end"))
	binary.BigEndian.PutUint32(tail, 0) // size 0: extends to end of file
	raw := cat(testFtyp, largeBox("moov", testMoov(elst(1), nil)[8:]), moof, mdat, box("moof", box("mfhd", u32(0), u32(2))), tail)

	initSeg, media, err := SplitFragmentedMP4(raw)
	if err != nil {
		t.Fatalf("SplitFragmentedMP4: %v", err)
	}
	if want := cat(testFtyp, testMoov(nil, nil)); !bytes.Equal(initSeg, want) {
		t.Fatal("largesize moov not rewritten correctly")
	}
	want := cat(fmp4Styp, moof, mdat, box("moof", box("mfhd", u32(0), u32(2))), box("mdat", []byte("to-end")))
	if !bytes.Equal(media, want) {
		t.Fatalf("media mismatch: types %v", topBoxTypes(t, media))
	}
}

func TestSplitFMP4ShiftsBaseDataOffset(t *testing.T) {
	head := cat(testFtyp, testMoov(nil, nil))
	// base_data_offset points at the moof itself (absolute file offset).
	raw := cat(head, testMoof(1, 0x000001, u64(uint64(len(head)))), box("mdat", []byte("x")))
	_, media, err := SplitFragmentedMP4(raw)
	if err != nil {
		t.Fatal(err)
	}
	moof := media[len(fmp4Styp):]
	tfhd, ok, err := findFMP4Path(mustBoxes(t, moof), "moof", "traf", "tfhd")
	if err != nil || !ok {
		t.Fatalf("tfhd not found: %v", err)
	}
	if got := binary.BigEndian.Uint64(tfhd.payload()[8:]); got != uint64(len(fmp4Styp)) {
		t.Fatalf("base_data_offset = %d, want %d", got, len(fmp4Styp))
	}
}

func mustBoxes(t *testing.T, b []byte) []fmp4Box {
	t.Helper()
	boxes, err := readFMP4Boxes(b)
	if err != nil {
		t.Fatal(err)
	}
	return boxes
}

func TestSplitFMP4Malformed(t *testing.T) {
	valid := cat(testFtyp, testMoov(elst(1), nil), testMoof(1, 0x020000), box("mdat", []byte("abc")))
	deep := box("mdia")
	for range 40 {
		deep = box("trak", deep)
	}
	oversized := cat(testFtyp, u32(1000), []byte("moov"), make([]byte, 20))
	innerOverflow := cat(testFtyp, box("moov", box("trak", u32(500), []byte("mdia"))), testMoof(1, 0), box("mdat"))

	cases := map[string][]byte{
		"empty":             nil,
		"truncated header":  []byte{0, 0, 0, 16, 'f'},
		"size below header": cat(u32(4), []byte("ftyp"), make([]byte, 8)),
		"size beyond data":  oversized,
		"largesize short":   cat(u32(1), []byte("moov"), u32(0)),
		"largesize beyond":  cat(u32(1), []byte("moov"), u64(1<<40)),
		"largesize too low": cat(u32(1), []byte("moov"), u64(12), make([]byte, 8)),
		"inner overflow":    innerOverflow,
		"deep nesting":      cat(testFtyp, box("moov", deep), testMoof(1, 0), box("mdat")),
		"no moov":           cat(testFtyp, testMoof(1, 0), box("mdat")),
		"no moof":           cat(testFtyp, testMoov(nil, nil)),
		"trailing garbage":  cat(valid, []byte{0, 0, 0}),
		"truncated tfhd":    cat(testFtyp, testMoov(nil, nil), box("moof", box("traf", box("tfhd", u32(1), u32(1), u32(0))))),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			initSeg, media, err := SplitFragmentedMP4(raw)
			if err == nil {
				t.Fatalf("expected error, got init=%d media=%d bytes", len(initSeg), len(media))
			}
			if strings.Contains(err.Error(), "malformed input") {
				t.Fatalf("error came from a recovered panic: %v", err)
			}
			if initSeg != nil || media != nil {
				t.Fatal("non-nil output on error")
			}
		})
	}
	if _, _, err := HLSCodecString(cat(testFtyp, box("moov", deep))); err == nil {
		t.Fatal("HLSCodecString accepted deep nesting")
	}
}

// TestFMP4TruncationNoPanic feeds every prefix and single-byte corruption of a
// valid file through the exported functions; none may panic.
func TestFMP4TruncationNoPanic(t *testing.T) {
	dvEntry := visualEntry("dvh1", box("hvcC", testHvcC(0x02, 0x20000000, 0xb0, 153)), box("dvcC", []byte{1, 0, 0x10, 0x35, 0x10, 0, 0, 0}))
	moov := box("moov", testTrak(1, "vide", dvEntry, elst(1)), testTrak(2, "soun", testAacEntry, nil))
	head := cat(testFtyp, moov)
	valid := cat(head, testMoof(1, 0x000001, u64(uint64(len(head)))), box("mdat", []byte("abc")))
	check := func(b []byte) {
		if _, _, err := SplitFragmentedMP4(b); err != nil && strings.Contains(err.Error(), "malformed input") {
			t.Fatalf("SplitFragmentedMP4 panicked: %v", err)
		}
		if _, _, err := HLSCodecString(b); err != nil && strings.Contains(err.Error(), "malformed input") {
			t.Fatalf("HLSCodecString panicked: %v", err)
		}
		DolbyVisionCodecString(b)
	}
	for n := range len(valid) {
		check(valid[:n])
	}
	buf := make([]byte, len(valid))
	for i := range valid {
		for _, v := range []byte{0x00, 0x01, 0x7f, 0xff} {
			copy(buf, valid)
			buf[i] = v
			check(buf)
		}
	}
}

func FuzzFMP4Split(f *testing.F) {
	f.Add(cat(testFtyp, testMoov(elst(1), nil), testMoof(1, 0x020000), box("mdat", []byte("abc"))))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if _, _, err := SplitFragmentedMP4(raw); err != nil && strings.Contains(err.Error(), "malformed input") {
			t.Fatal(err)
		}
		if _, _, err := HLSCodecString(raw); err != nil && strings.Contains(err.Error(), "malformed input") {
			t.Fatal(err)
		}
		DolbyVisionCodecString(raw)
	})
}

// testHvcC builds an hvcC record header with one leading constraint byte.
func testHvcC(profileByte byte, compat uint32, constraint0, level byte) []byte {
	return cat([]byte{1, profileByte}, u32(compat), []byte{constraint0, 0, 0, 0, 0, 0, level, 0xf0, 0, 0xfc, 0xfd, 0xf8, 0xf8, 0, 0, 0x0f, 0})
}

func codecInit(video, audio []byte) []byte {
	var traks [][]byte
	if video != nil {
		traks = append(traks, testTrak(1, "vide", video, nil))
	}
	if audio != nil {
		traks = append(traks, testTrak(2, "soun", audio, nil))
	}
	return cat(testFtyp, box("moov", traks...))
}

func TestHLSCodecStringVideo(t *testing.T) {
	hevc6 := func(b [6]byte) []byte {
		p := testHvcC(0x01, 0x60000000, 0, 93)
		copy(p[6:12], b[:])
		return p
	}
	cases := []struct {
		name  string
		entry []byte
		want  string
	}{
		{"avc high 3.1", testAvcEntry, "avc1.64001f"},
		{"avc3 baseline", visualEntry("avc3", box("avcC", []byte{1, 0x42, 0xc0, 0x1e})), "avc3.42c01e"},
		{"hevc main10 4.1", visualEntry("hvc1", box("hvcC", testHvcC(0x02, 0x20000000, 0xb0, 123))), "hvc1.2.4.L123.B0"},
		{"hevc main 3.1", visualEntry("hvc1", box("hvcC", testHvcC(0x01, 0x60000000, 0xb0, 93))), "hvc1.1.6.L93.B0"},
		{"hevc main 4 constraint 90", visualEntry("hvc1", box("hvcC", testHvcC(0x01, 0x60000000, 0x90, 120))), "hvc1.1.6.L120.90"},
		{"hev1 high tier 5.0", visualEntry("hev1", box("hvcC", testHvcC(0x22, 0x20000000, 0xb0, 150))), "hev1.2.4.H150.B0"},
		{"hevc profile space A", visualEntry("hev1", box("hvcC", testHvcC(0x41, 0x01000000, 0xb0, 93))), "hev1.A1.80.L93.B0"},
		{"hevc no constraints", visualEntry("hvc1", box("hvcC", hevc6([6]byte{}))), "hvc1.1.6.L93"},
		{"hevc inner zero constraint", visualEntry("hvc1", box("hvcC", hevc6([6]byte{0xb0, 0, 0x23}))), "hvc1.1.6.L93.B0.0.23"},
		{"dvh1 base layer", visualEntry("dvh1", box("hvcC", testHvcC(0x02, 0x20000000, 0xb0, 153)), box("dvcC", make([]byte, 24))), "hvc1.2.4.L153.B0"},
		{"av1 main 10bit", visualEntry("av01", box("av1C", []byte{0x81, 0x08, 0x40, 0x00})), "av01.0.08M.10"},
		{"av1 8bit", visualEntry("av01", box("av1C", []byte{0x81, 0x05, 0x0c, 0x00})), "av01.0.05M.08"},
		{"av1 professional 12bit high", visualEntry("av01", box("av1C", []byte{0x81, 0x4d, 0xe0, 0x00})), "av01.2.13H.12"},
		{"encrypted avc", visualEntry("encv", box("avcC", []byte{1, 0x4d, 0x40, 0x28}), box("sinf", box("frma", []byte("avc1")))), "avc1.4d4028"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			video, audio, err := HLSCodecString(codecInit(tc.entry, nil))
			if err != nil {
				t.Fatalf("HLSCodecString: %v", err)
			}
			if video != tc.want || audio != "" {
				t.Fatalf("got (%q, %q), want (%q, \"\")", video, audio, tc.want)
			}
		})
	}
}

func TestHLSCodecStringAudio(t *testing.T) {
	v1 := box("mp4a", make([]byte, 8), []byte{0, 1}, make([]byte, 18+16), box("esds", u32(0), testESDescriptor(0x40, []byte{0x2b, 0x92, 0x08, 0x00})))
	cases := []struct {
		name  string
		entry []byte
		want  string
	}{
		{"aac lc", testAacEntry, "mp4a.40.2"},
		{"he-aac", audioEntry("mp4a", box("esds", u32(0), testESDescriptor(0x40, []byte{0x2b, 0x92, 0x08, 0x00}))), "mp4a.40.5"},
		{"escaped aot 42", audioEntry("mp4a", box("esds", u32(0), testESDescriptor(0x40, []byte{0xf9, 0x40, 0x00}))), "mp4a.40.42"},
		{"aac without dsi", audioEntry("mp4a", box("esds", u32(0), testESDescriptor(0x40, nil))), "mp4a.40.2"},
		{"aac without esds", audioEntry("mp4a"), "mp4a.40.2"},
		{"mp3 in mp4a", audioEntry("mp4a", box("esds", u32(0), testESDescriptor(0x6b, nil))), "mp4a.6B"},
		{"mp3 entry", audioEntry(".mp3"), "mp4a.6B"},
		{"version 1 entry", v1, "mp4a.40.5"},
		{"ac-3", audioEntry("ac-3", box("dac3", []byte{0x10, 0x3d, 0xc0})), "ac-3"},
		{"ec-3", audioEntry("ec-3", box("dec3", []byte{0x0c, 0x00, 0x20, 0x0f, 0x00})), "ec-3"},
		{"opus", audioEntry("Opus", box("dOps", make([]byte, 11))), "opus"},
		{"flac", audioEntry("fLaC", box("dfLa", make([]byte, 4))), "fLaC"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			video, audio, err := HLSCodecString(codecInit(testAvcEntry, tc.entry))
			if err != nil {
				t.Fatalf("HLSCodecString: %v", err)
			}
			if video != "avc1.64001f" || audio != tc.want {
				t.Fatalf("got (%q, %q), want (avc1.64001f, %q)", video, audio, tc.want)
			}
		})
	}
}

func TestHLSCodecStringErrors(t *testing.T) {
	video, audio, err := HLSCodecString(codecInit(visualEntry("mp4v"), testAacEntry))
	if err == nil || video != "" || audio != "mp4a.40.2" {
		t.Fatalf("unsupported video: got (%q, %q, %v)", video, audio, err)
	}
	video, audio, err = HLSCodecString(codecInit(testAvcEntry, audioEntry("alac")))
	if err == nil || video != "avc1.64001f" || audio != "" {
		t.Fatalf("unsupported audio: got (%q, %q, %v)", video, audio, err)
	}
	if _, _, err := HLSCodecString(codecInit(visualEntry("hvc1"), nil)); err == nil {
		t.Fatal("hvc1 without hvcC accepted")
	}
	if _, _, err := HLSCodecString(testFtyp); err == nil {
		t.Fatal("missing moov accepted")
	}
	if _, _, err := HLSCodecString(cat(testFtyp, box("moov"))); err == nil {
		t.Fatal("moov without tracks accepted")
	}
	if _, _, err := HLSCodecString(codecInit(box("avc1", make([]byte, 40)), nil)); err == nil {
		t.Fatal("truncated visual sample entry accepted")
	}
}

func TestDolbyVisionCodecString(t *testing.T) {
	hvcC := box("hvcC", testHvcC(0x02, 0x20000000, 0xb0, 153))
	// dv_profile 8, dv_level 6, rpu+bl present, bl_signal_compatibility_id 1.
	p8 := []byte{1, 0, 0x10, 0x35, 0x10, 0, 0, 0}
	// dv_profile 5, dv_level 9, compatibility 0.
	p5 := []byte{1, 0, 0x0a, 0x4d, 0x00, 0, 0, 0}
	cases := []struct {
		name   string
		entry  []byte
		want   string
		compat int
		ok     bool
	}{
		{"hvc1 profile 8.1", visualEntry("hvc1", hvcC, box("dvcC", p8)), "dvh1.08.06", 1, true},
		{"dvh1 profile 5", visualEntry("dvh1", hvcC, box("dvcC", p5)), "dvh1.05.09", 0, true},
		{"dvhe dvvC", visualEntry("dvhe", hvcC, box("dvvC", p8)), "dvhe.08.06", 1, true},
		{"hev1 profile 8", visualEntry("hev1", hvcC, box("dvcC", p8)), "dvhe.08.06", 1, true},
		{"av1 profile 10", visualEntry("av01", box("av1C", []byte{0x81, 0x08, 0x40, 0}), box("dvvC", []byte{1, 0, 0x14, 0x35, 0x10})), "dav1.10.06", 1, true},
		{"no dv config", visualEntry("hvc1", hvcC), "", 0, false},
		{"short dv config", visualEntry("hvc1", hvcC, box("dvcC", []byte{1, 0, 0x10})), "", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			codec, compat, ok := DolbyVisionCodecString(codecInit(tc.entry, testAacEntry))
			if codec != tc.want || compat != tc.compat || ok != tc.ok {
				t.Fatalf("got (%q, %d, %v), want (%q, %d, %v)", codec, compat, ok, tc.want, tc.compat, tc.ok)
			}
		})
	}
	if _, _, ok := DolbyVisionCodecString([]byte{0, 0, 0, 99}); ok {
		t.Fatal("garbage accepted")
	}
}

// TestSplitFMP4Fixture uses two consecutive segments produced by ffmpeg's
// segment muxer (HEVC Main10 with B-frames + AAC); segment 0 carries a
// B-frame delay edit list.
func TestSplitFMP4Fixture(t *testing.T) {
	var inits, moovs [2][]byte
	for i := range inits {
		raw, err := os.ReadFile(filepath.Join("testdata", "fmp4", fmt.Sprintf("hevc_aac_seg%d.mp4", i)))
		if err != nil {
			t.Fatal(err)
		}
		initSeg, media, err := SplitFragmentedMP4(raw)
		if err != nil {
			t.Fatalf("segment %d: %v", i, err)
		}
		inits[i] = initSeg
		if moov, ok := findFMP4Box(mustBoxes(t, raw), "moov"); ok {
			moovs[i] = moov.data
		}
		if err := checkFMP4Tree(initSeg, 0); err != nil {
			t.Fatalf("segment %d init: %v", i, err)
		}
		if bytes.Contains(initSeg, []byte("edts")) {
			t.Fatalf("segment %d init contains edts", i)
		}

		var want []byte
		moofs := 0
		for _, b := range mustBoxes(t, raw) {
			switch b.typ {
			case "moof":
				moofs++
				fallthrough
			case "mdat":
				want = append(want, b.data...)
			}
		}
		types := topBoxTypes(t, media)
		if types[0] != "styp" || moofs == 0 || !bytes.Equal(media[len(fmp4Styp):], want) {
			t.Fatalf("segment %d media boxes %v do not match source moof/mdat", i, types)
		}

		video, audio, err := HLSCodecString(initSeg)
		if err != nil {
			t.Fatalf("segment %d HLSCodecString: %v", i, err)
		}
		if !strings.HasPrefix(video, "hvc1.2.4.L") || audio != "mp4a.40.2" {
			t.Fatalf("segment %d codecs = (%q, %q)", i, video, audio)
		}
		t.Logf("segment %d: video=%s audio=%s", i, video, audio)
		if _, _, ok := DolbyVisionCodecString(initSeg); ok {
			t.Fatalf("segment %d reports Dolby Vision", i)
		}
	}
	if bytes.Equal(moovs[0], moovs[1]) {
		t.Fatal("fixture moovs are identical; the edit-list case is not exercised")
	}
	if !bytes.Equal(inits[0], inits[1]) {
		t.Fatal("init segments of consecutive segments differ")
	}
}
