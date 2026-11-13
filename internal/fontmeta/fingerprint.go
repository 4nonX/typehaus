package fontmeta

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
)

// FingerprintVersion prefixes every fingerprint. Stored fingerprints with
// another prefix were made by an older rule and are computed again.
const FingerprintVersion = "2."

// fingerprint identifies a face by what defines it rather than by its file:
// names, version, vendor (OS/2 vendor ID and manufacturer), the license and
// copyright notices, units per em, glyph count, the full character-to-glyph
// mapping, every advance width, weight/width/italic and variable axes. Fonts
// that share a name but come from another foundry or under other terms are
// therefore never taken for the same font.
//
// It is the same for the same font in different files – a WOFF2 decompressed
// to TTF, a re-saved or re-signed copy – and different for every weight,
// width, optical size, style or version, even when the family names repeat.
// It does not look at the glyph outlines themselves, so a hinted and an
// unhinted build of the same version count as the same font.
func fingerprint(b []byte, tables map[string]table, names map[int]string) string {
	h := sha256.New()
	put := func(k, v string) {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(v))
		h.Write([]byte{0})
	}
	// family, style, version, PostScript name; copyright, manufacturer and
	// the license description and URL
	for _, id := range []int{1, 2, 5, 6, 16, 17, 0, 8, 13, 14} {
		put("name"+strconv.Itoa(id), strings.TrimSpace(names[id]))
	}
	if t, ok := tables["head"]; ok && t.length >= 20 {
		put("upem", strconv.Itoa(int(be16(b, t.off+18))))
	}
	numGlyphs := 0
	if t, ok := tables["maxp"]; ok && t.length >= 6 {
		numGlyphs = int(be16(b, t.off+4))
		put("glyphs", strconv.Itoa(numGlyphs))
	}
	if t, ok := tables["OS/2"]; ok && t.length >= 64 {
		put("weight", strconv.Itoa(int(be16(b, t.off+4))))
		put("width", strconv.Itoa(int(be16(b, t.off+6))))
		put("fsSelection", strconv.Itoa(int(be16(b, t.off+62)&(0x1|0x20|0x200))))
		put("vendor", strings.TrimSpace(strings.Trim(string(b[t.off+58:t.off+62]), "\x00")))
	}
	// advance widths (not the side bearings: WOFF2 may rebuild those)
	if hh, ok := tables["hhea"]; ok && hh.length >= 36 {
		if hm, ok := tables["hmtx"]; ok {
			n := int(be16(b, hh.off+34))
			if n > numGlyphs && numGlyphs > 0 {
				n = numGlyphs
			}
			h.Write([]byte("hmtx"))
			for i := 0; i < n && 4*i+2 <= hm.length; i++ {
				h.Write(b[hm.off+4*i : hm.off+4*i+2])
			}
		}
	}
	if t, ok := tables["cmap"]; ok {
		pairs := cmapPairs(b[t.off : t.off+t.length])
		h.Write([]byte("cmap"))
		var buf [8]byte
		for _, p := range pairs {
			binary.BigEndian.PutUint32(buf[:4], p[0])
			binary.BigEndian.PutUint32(buf[4:], p[1])
			h.Write(buf[:])
		}
	}
	if t, ok := tables["fvar"]; ok {
		h.Write([]byte("fvar"))
		h.Write(b[t.off : t.off+t.length])
	}
	sum := h.Sum(nil)
	return FingerprintVersion + hex.EncodeToString(sum[:16])
}

// cmapPairs decodes the best Unicode subtable of a cmap into sorted
// (code point, glyph id) pairs, so that the same mapping stored in a
// different subtable format still compares equal.
func cmapPairs(t []byte) [][2]uint32 {
	if len(t) < 4 {
		return nil
	}
	n := int(be16(t, 2))
	best, bestRank := -1, 0
	for i := 0; i < n; i++ {
		r := 4 + 8*i
		if r+8 > len(t) {
			break
		}
		pid, eid, off := be16(t, r), be16(t, r+2), int(be32(t, r+4))
		if off < 0 || off+2 > len(t) {
			continue
		}
		format := be16(t, off)
		rank := 0
		switch {
		case format == 12 && (pid == 3 && eid == 10 || pid == 0):
			rank = 3
		case format == 4 && (pid == 3 && eid == 1 || pid == 0):
			rank = 2
		case format == 4 || format == 12:
			rank = 1
		}
		if rank > bestRank {
			best, bestRank = off, rank
		}
	}
	if best < 0 {
		return nil
	}
	var out [][2]uint32
	switch be16(t, best) {
	case 4:
		out = cmap4(t[best:])
	case 12:
		out = cmap12(t[best:])
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

func cmap4(s []byte) [][2]uint32 {
	if len(s) < 14 {
		return nil
	}
	segX2 := int(be16(s, 6))
	ends, starts := 14, 14+segX2+2
	deltas, ranges := starts+segX2, starts+2*segX2
	if ranges+segX2 > len(s) {
		return nil
	}
	var out [][2]uint32
	for i := 0; i < segX2/2; i++ {
		end, start := int(be16(s, ends+2*i)), int(be16(s, starts+2*i))
		delta := int(be16(s, deltas+2*i))
		ro := int(be16(s, ranges+2*i))
		for c := start; c <= end && c != 0xFFFF; c++ {
			g := 0
			if ro == 0 {
				g = (c + delta) & 0xFFFF
			} else {
				addr := ranges + 2*i + ro + 2*(c-start)
				if addr+2 > len(s) {
					break
				}
				if g = int(be16(s, addr)); g != 0 {
					g = (g + delta) & 0xFFFF
				}
			}
			if g != 0 {
				out = append(out, [2]uint32{uint32(c), uint32(g)})
			}
		}
	}
	return out
}

func cmap12(s []byte) [][2]uint32 {
	if len(s) < 16 {
		return nil
	}
	n := int(be32(s, 12))
	var out [][2]uint32
	total := 0
	for i := 0; i < n; i++ {
		r := 16 + 12*i
		if r+12 > len(s) {
			break
		}
		start, end, g := be32(s, r), be32(s, r+4), be32(s, r+8)
		if end < start || end > 0x10FFFF {
			continue
		}
		for c := start; c <= end; c++ {
			out = append(out, [2]uint32{c, g + (c - start)})
			if total++; total > 0x110000 {
				return out
			}
		}
	}
	return out
}
