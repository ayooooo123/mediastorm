package handlers

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func encodeTestImage(t *testing.T, img image.Image, format string) []byte {
	t.Helper()
	var data bytes.Buffer
	var err error
	if format == "png" {
		err = png.Encode(&data, img)
	} else {
		err = jpeg.Encode(&data, img, &jpeg.Options{Quality: 78})
	}
	if err != nil {
		t.Fatalf("encode test image: %v", err)
	}
	return data.Bytes()
}

func solidTestImage(fill color.Color) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 8, 12))
	for y := 0; y < 12; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, fill)
		}
	}
	return img
}

func TestNormalizeProxyWidth(t *testing.T) {
	tests := []struct {
		name  string
		width int
		want  int
	}{
		{name: "4K width", width: 3840, want: 3840},
		{name: "above 4K", width: 3841, want: 3840},
		{name: "unset", width: 0, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeProxyWidth(tt.width); got != tt.want {
				t.Fatalf("normalizeProxyWidth(%d) = %d, want %d", tt.width, got, tt.want)
			}
		})
	}
}

func TestIsBlankProxyImageData(t *testing.T) {
	tests := []struct {
		name   string
		image  image.Image
		format string
		blank  bool
	}{
		{name: "black JPEG", image: solidTestImage(color.Black), format: "jpeg", blank: true},
		{name: "transparent PNG", image: solidTestImage(color.Transparent), format: "png", blank: true},
		{name: "visible artwork", image: solidTestImage(color.RGBA{R: 40, G: 20, B: 10, A: 255}), format: "jpeg", blank: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBlankProxyImageData(encodeTestImage(t, tt.image, tt.format)); got != tt.blank {
				t.Fatalf("isBlankProxyImageData() = %v, want %v", got, tt.blank)
			}
		})
	}
}

func TestIsBlankProxyImageDataTreatsDecodeFailureAsNonBlank(t *testing.T) {
	if isBlankProxyImageData([]byte("not an image")) {
		t.Fatal("decode failures must be handled by the normal image error path")
	}
}
