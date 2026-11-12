package brand

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
)

// PNG encodes img as PNG.
func PNG(img image.Image) []byte {
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

// ICO packs PNG-compressed images into a Windows .ico container
// (supported since Windows Vista).
func ICO(imgs ...image.Image) []byte {
	var hdr, data bytes.Buffer
	binary.Write(&hdr, binary.LittleEndian, [3]uint16{0, 1, uint16(len(imgs))})
	offset := 6 + 16*len(imgs)
	for _, img := range imgs {
		p := PNG(img)
		w, h := img.Bounds().Dx(), img.Bounds().Dy()
		dim := func(v int) byte {
			if v >= 256 {
				return 0
			}
			return byte(v)
		}
		hdr.Write([]byte{dim(w), dim(h), 0, 0})
		binary.Write(&hdr, binary.LittleEndian, uint16(1))  // planes
		binary.Write(&hdr, binary.LittleEndian, uint16(32)) // bpp
		binary.Write(&hdr, binary.LittleEndian, uint32(len(p)))
		binary.Write(&hdr, binary.LittleEndian, uint32(offset))
		offset += len(p)
		data.Write(p)
	}
	hdr.Write(data.Bytes())
	return hdr.Bytes()
}
