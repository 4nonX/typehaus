package brand

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"testing"
)

func TestMarkAllStates(t *testing.T) {
	for _, px := range []int{16, 20, 24, 32, 64, 256} {
		for v := OnDark; v <= Tile; v++ {
			for s := StateOK; s <= StateDisconnected; s++ {
				img := Mark(px, v, s, 1)
				if img.Bounds().Dx() != px || img.Bounds().Dy() != px {
					t.Fatalf("size %d: got %v", px, img.Bounds())
				}
				var opaque int
				for i := 3; i < len(img.Pix); i += 4 {
					if img.Pix[i] > 0 {
						opaque++
					}
				}
				if opaque < px*px/12 {
					t.Errorf("px=%d v=%d s=%d: mark nearly empty (%d px)", px, v, s, opaque)
				}
			}
		}
	}
}

// The status stop must stay a perfect square at small sizes.
func TestStopIsSquare(t *testing.T) {
	for _, px := range []int{16, 20, 24, 32} {
		img := Mark(px, Template, StateOK, 0)
		minX, maxX, minY, maxY := px, -1, px, -1
		for y := 0; y < px; y++ {
			for x := px * 3 / 4; x < px; x++ { // right quarter holds only the stop
				if img.NRGBAAt(x, y).A == 255 {
					minX, maxX = min(minX, x), max(maxX, x)
					minY, maxY = min(minY, y), max(maxY, y)
				}
			}
		}
		if w, h := maxX-minX+1, maxY-minY+1; w != h {
			t.Errorf("px=%d: stop is %dx%d", px, w, h)
		}
	}
}

func TestICO(t *testing.T) {
	ico := ICO(Mark(16, OnDark, StateOK, 0), Mark(256, Tile, StateOK, 0))
	if binary.LittleEndian.Uint16(ico[2:]) != 1 || binary.LittleEndian.Uint16(ico[4:]) != 2 {
		t.Fatal("bad ICO header")
	}
	off := binary.LittleEndian.Uint32(ico[6+16+12:])
	if _, err := png.Decode(bytes.NewReader(ico[off:])); err != nil {
		t.Fatalf("second entry is not a PNG: %v", err)
	}
	if ico[6+16] != 0 { // 256 px is stored as 0
		t.Fatal("256 px width must be encoded as 0")
	}
}
