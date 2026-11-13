package fontmeta

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"

	"github.com/andybalholm/brotli"
)

// Web font encoding: an OpenType/TrueType font (or one face of a collection)
// as WOFF2 and WOFF 1.0 for browsers. WOFF2 tables are stored without the
// optional glyf/loca/hmtx transforms ("null transform", allowed by the
// WOFF2 specification); browsers decode that like any WOFF2 file.

type sfntTable struct {
	tag      string
	checksum uint32
	data     []byte
}

// sfntTables reads the tables of a single font (index selects the face of a
// TrueType/OpenType collection).
func sfntTables(b []byte, index int) (uint32, []sfntTable, error) {
	if len(b) < 12 {
		return 0, nil, errors.New("not an OpenType font")
	}
	off := uint32(0)
	if string(b[:4]) == "ttcf" {
		if len(b) < 12 {
			return 0, nil, errors.New("truncated collection")
		}
		n := binary.BigEndian.Uint32(b[8:])
		if index < 0 || uint32(index) >= n || len(b) < 12+4*int(n) {
			return 0, nil, fmt.Errorf("the collection has no face %d", index)
		}
		off = binary.BigEndian.Uint32(b[12+4*index:])
	} else if index != 0 {
		return 0, nil, fmt.Errorf("not a collection, no face %d", index)
	}
	if int(off)+12 > len(b) {
		return 0, nil, errors.New("truncated font")
	}
	flavor := binary.BigEndian.Uint32(b[off:])
	switch flavor {
	case 0x00010000, 0x4F54544F, 0x74727565: // TrueType, 'OTTO', 'true'
	default:
		return 0, nil, errors.New("not a TrueType or OpenType font")
	}
	num := int(binary.BigEndian.Uint16(b[off+4:]))
	if int(off)+12+16*num > len(b) {
		return 0, nil, errors.New("truncated table directory")
	}
	tables := make([]sfntTable, 0, num)
	for i := 0; i < num; i++ {
		r := b[int(off)+12+16*i:]
		tag := string(r[:4])
		start, length := binary.BigEndian.Uint32(r[8:]), binary.BigEndian.Uint32(r[12:])
		if uint64(start)+uint64(length) > uint64(len(b)) {
			return 0, nil, fmt.Errorf("table %q lies outside the file", tag)
		}
		if tag == "DSIG" {
			continue // a signature doesn't survive re-encoding
		}
		tables = append(tables, sfntTable{tag: tag, checksum: binary.BigEndian.Uint32(r[4:]), data: b[start : start+length]})
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].tag < tables[j].tag })
	return flavor, tables, nil
}

func pad4(n int) int { return (n + 3) &^ 3 }

// putBE writes big-endian values: uint16, uint32 or raw bytes.
func putBE(w *bytes.Buffer, vals ...any) {
	var b [4]byte
	for _, v := range vals {
		switch x := v.(type) {
		case uint16:
			binary.BigEndian.PutUint16(b[:2], x)
			w.Write(b[:2])
		case uint32:
			binary.BigEndian.PutUint32(b[:], x)
			w.Write(b[:])
		case []byte:
			w.Write(x)
		default:
			panic(fmt.Sprintf("putBE: %T", v))
		}
	}
}

func sfntSize(tables []sfntTable) uint32 {
	n := 12 + 16*len(tables)
	for _, t := range tables {
		n += pad4(len(t.data))
	}
	return uint32(n)
}

// ExtractFace returns one face of a collection as a standalone font (a
// single font is returned as is).
func ExtractFace(b []byte, index int) ([]byte, error) {
	if len(b) >= 4 && string(b[:4]) != "ttcf" && index == 0 {
		return b, nil
	}
	flavor, tables, err := sfntTables(b, index)
	if err != nil {
		return nil, err
	}
	var w bytes.Buffer
	num := len(tables)
	es := 0
	for 1<<(es+1) <= num {
		es++
	}
	sr := (1 << es) * 16
	putBE(&w, flavor, uint16(num), uint16(sr), uint16(es), uint16(num*16-sr))
	off := 12 + 16*num
	for _, t := range tables {
		putBE(&w, []byte(t.tag), t.checksum, uint32(off), uint32(len(t.data)))
		off += pad4(len(t.data))
	}
	for _, t := range tables {
		w.Write(t.data)
		w.Write(make([]byte, pad4(len(t.data))-len(t.data)))
	}
	return w.Bytes(), nil
}

// EncodeWOFF encodes a font (one face) as WOFF 1.0.
func EncodeWOFF(b []byte, index int) ([]byte, error) {
	flavor, tables, err := sfntTables(b, index)
	if err != nil {
		return nil, err
	}
	type entry struct {
		t    sfntTable
		data []byte
	}
	var es []entry
	for _, t := range tables {
		var z bytes.Buffer
		zw, _ := zlib.NewWriterLevel(&z, zlib.BestCompression)
		zw.Write(t.data)
		zw.Close()
		d := t.data
		if z.Len() < len(t.data) {
			d = z.Bytes()
		}
		es = append(es, entry{t, d})
	}
	off := 44 + 20*len(es)
	var dir, body bytes.Buffer
	for _, e := range es {
		putBE(&dir, []byte(e.t.tag), uint32(off), uint32(len(e.data)), uint32(len(e.t.data)), e.t.checksum)
		body.Write(e.data)
		body.Write(make([]byte, pad4(len(e.data))-len(e.data)))
		off += pad4(len(e.data))
	}
	var w bytes.Buffer
	putBE(&w, []byte("wOFF"), flavor, uint32(off), uint16(len(es)), uint16(0),
		sfntSize(tables), uint16(1), uint16(0), uint32(0), uint32(0), uint32(0), uint32(0), uint32(0))
	w.Write(dir.Bytes())
	w.Write(body.Bytes())
	return w.Bytes(), nil
}

var woff2KnownTags = []string{"cmap", "head", "hhea", "hmtx", "maxp", "name", "OS/2", "post", "cvt ", "fpgm", "glyf", "loca", "prep",
	"CFF ", "VORG", "EBDT", "EBLC", "gasp", "hdmx", "kern", "LTSH", "PCLT", "VDMX", "vhea", "vmtx", "BASE", "GDEF", "GPOS", "GSUB",
	"EBSC", "JSTF", "MATH", "CBDT", "CBLC", "COLR", "CPAL", "SVG ", "sbix", "acnt", "avar", "bdat", "bloc", "bsln", "cvar", "fdsc",
	"feat", "fmtx", "fvar", "gvar", "hsty", "just", "lcar", "mort", "morx", "opbd", "prop", "trak", "Zapf", "Silf", "Glat", "Gloc",
	"Feat", "Sill"}

func base128(w *bytes.Buffer, v uint32) {
	var tmp [5]byte
	n := 0
	for {
		tmp[4-n] = byte(v & 0x7f)
		n++
		v >>= 7
		if v == 0 {
			break
		}
	}
	for i := 5 - n; i < 5; i++ {
		b := tmp[i]
		if i != 4 {
			b |= 0x80
		}
		w.WriteByte(b)
	}
}

// EncodeWOFF2 encodes a font (one face) as WOFF2.
func EncodeWOFF2(b []byte, index int) ([]byte, error) {
	flavor, tables, err := sfntTables(b, index)
	if err != nil {
		return nil, err
	}
	known := map[string]int{}
	for i, t := range woff2KnownTags {
		known[t] = i
	}
	var dir, raw bytes.Buffer
	for _, t := range tables {
		flags := byte(63)
		if i, ok := known[t.tag]; ok {
			flags = byte(i)
		}
		if t.tag == "glyf" || t.tag == "loca" {
			flags |= 3 << 6 // transform version 3: stored as is
		}
		data := t.data
		if t.tag == "head" && len(data) >= 18 {
			// bit 11: the font went through a lossless re-encoding
			data = append([]byte{}, data...)
			binary.BigEndian.PutUint16(data[16:], binary.BigEndian.Uint16(data[16:])|1<<11)
		}
		dir.WriteByte(flags)
		if flags&63 == 63 {
			dir.WriteString(t.tag)
		}
		base128(&dir, uint32(len(data)))
		raw.Write(data)
	}
	var comp bytes.Buffer
	bw := brotli.NewWriterOptions(&comp, brotli.WriterOptions{Quality: 11, LGWin: 22})
	if _, err := bw.Write(raw.Bytes()); err != nil {
		return nil, err
	}
	if err := bw.Close(); err != nil {
		return nil, err
	}
	total := 48 + dir.Len() + comp.Len()
	var w bytes.Buffer
	putBE(&w, []byte("wOF2"), flavor, uint32(pad4(total)), uint16(len(tables)), uint16(0),
		sfntSize(tables), uint32(comp.Len()), uint16(1), uint16(0), uint32(0), uint32(0), uint32(0), uint32(0), uint32(0))
	w.Write(dir.Bytes())
	w.Write(comp.Bytes())
	w.Write(make([]byte, pad4(total)-total))
	return w.Bytes(), nil
}

// WeightRange returns the weight axis range of a variable font (ok false
// for a static font).
func WeightRange(b []byte, index int) (lo, hi int, ok bool) {
	_, tables, err := sfntTables(b, index)
	if err != nil {
		return 0, 0, false
	}
	for _, t := range tables {
		if t.tag != "fvar" || len(t.data) < 16 {
			continue
		}
		d := t.data
		axesOff := int(binary.BigEndian.Uint16(d[4:]))
		count := int(binary.BigEndian.Uint16(d[8:]))
		size := int(binary.BigEndian.Uint16(d[10:]))
		for i := 0; i < count; i++ {
			p := axesOff + i*size
			if size < 20 || p+20 > len(d) {
				break
			}
			if string(d[p:p+4]) == "wght" {
				fix := func(o int) int { return int(int32(binary.BigEndian.Uint32(d[o:])) >> 16) }
				lo, hi = fix(p+4), fix(p+12)
				if lo >= 1 && hi <= 1000 && lo < hi {
					return lo, hi, true
				}
			}
		}
	}
	return 0, 0, false
}
