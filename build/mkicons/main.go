// Command mkicons spreads the brand mark across everything the agent shows.
//
// Source of truth is favicon.svg in the repository root. From it this tool
// produces the icons for the tray and the executable, and refreshes the copy
// served with the streaming page, so the page, the tray and the window always
// carry the same mark.
//
// Run it from this directory after changing favicon.svg:
//
//	go run .
//
// The results are committed, so an ordinary build needs no rasterisation step.
// This is a separate module on purpose: the SVG rasteriser must not end up in
// the agent's go.mod.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"image/png"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
)

const (
	// source is the brand mark everything else is derived from.
	source = "../../favicon.svg"
	// pageCopy is served with the streaming page and embedded into the binary.
	pageCopy = "../../internal/webdefaults/default/icons/favicon.svg"
	// brandColor is the mark's own colour; each variant swaps it for its own,
	// which is how the tray shows stream health at a glance.
	brandColor = "#FF0000"
	// padding keeps the mark off the very edge of the icon box.
	padding = 0.92
)

var brandRe = regexp.MustCompile(`(?i)` + regexp.QuoteMeta(brandColor))

type variant struct {
	name  string
	color string
	note  string
}

func main() {
	svg, err := os.ReadFile(filepath.FromSlash(source))
	if err != nil {
		fail(err)
	}
	if !brandRe.MatchString(string(svg)) {
		fail(fmt.Errorf("в %s нет цвета %s — проверьте источник", source, brandColor))
	}

	// The page is served from disk, so its copy has to be refreshed too.
	if err := os.WriteFile(filepath.FromSlash(pageCopy), svg, 0o644); err != nil {
		fail(err)
	}
	fmt.Println("обновлено", pageCopy)

	variants := []variant{
		{"icon", brandColor, "всё в эфире"},
		{"icon-warn", "#F5A623", "часть камер без потока"},
		{"icon-down", "#8A94A0", "служба недоступна"},
	}

	outDirs := []string{
		filepath.FromSlash("../../internal/tray/icons"),
		filepath.FromSlash("../icons"),
	}
	for _, d := range outDirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			fail(err)
		}
	}

	for _, v := range variants {
		// oksvg keeps the parsed fill colour private, so the variant colour is
		// substituted in the source before parsing.
		recoloured := brandRe.ReplaceAllString(string(svg), v.color)

		ico, err := buildICO(recoloured, 16, 20, 24, 32, 48, 64, 256)
		if err != nil {
			fail(fmt.Errorf("%s: %w", v.name, err))
		}
		for _, d := range outDirs {
			if err := os.WriteFile(filepath.Join(d, v.name+".ico"), ico, 0o644); err != nil {
				fail(err)
			}
		}
		fmt.Printf("%-14s %-8s %s\n", v.name+".ico", v.color, v.note)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "ошибка:", err)
	os.Exit(1)
}

// buildICO rasterises the SVG at each size and packs the results into an .ico.
// Windows Vista and later read PNG-compressed entries, which keeps the file
// small and the 256px entry sharp.
func buildICO(svg string, sizes ...int) ([]byte, error) {
	type entry struct {
		size int
		data []byte
	}

	var entries []entry
	for _, s := range sizes {
		img, err := rasterise(svg, s)
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return nil, err
		}
		entries = append(entries, entry{size: s, data: buf.Bytes()})
	}

	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, uint16(0)) // reserved
	binary.Write(&out, binary.LittleEndian, uint16(1)) // type: icon
	binary.Write(&out, binary.LittleEndian, uint16(len(entries)))

	offset := 6 + 16*len(entries)
	for _, e := range entries {
		dim := byte(e.size)
		if e.size >= 256 {
			dim = 0 // 0 means 256 in the directory entry
		}
		out.WriteByte(dim) // width
		out.WriteByte(dim) // height
		out.WriteByte(0)   // palette size
		out.WriteByte(0)   // reserved
		binary.Write(&out, binary.LittleEndian, uint16(1))
		binary.Write(&out, binary.LittleEndian, uint16(32))
		binary.Write(&out, binary.LittleEndian, uint32(len(e.data)))
		binary.Write(&out, binary.LittleEndian, uint32(offset))
		offset += len(e.data)
	}
	for _, e := range entries {
		out.Write(e.data)
	}
	return out.Bytes(), nil
}

// rasterise draws the mark centred in a square box. The mark is wider than it
// is tall, and SetTarget scales each axis on its own, so the target rectangle
// is computed here to keep the proportions.
func rasterise(svg string, size int) (image.Image, error) {
	icon, err := oksvg.ReadIconStream(strings.NewReader(svg), oksvg.WarnErrorMode)
	if err != nil {
		return nil, err
	}
	if icon.ViewBox.W <= 0 || icon.ViewBox.H <= 0 {
		return nil, fmt.Errorf("в SVG нет размеров viewBox")
	}

	box := float64(size) * padding
	scale := math.Min(box/icon.ViewBox.W, box/icon.ViewBox.H)
	w, h := icon.ViewBox.W*scale, icon.ViewBox.H*scale
	icon.SetTarget((float64(size)-w)/2, (float64(size)-h)/2, w, h)

	img := image.NewRGBA(image.Rect(0, 0, size, size))
	scanner := rasterx.NewScannerGV(size, size, img, img.Bounds())
	icon.Draw(rasterx.NewDasher(size, size, scanner), 1.0)
	return img, nil
}
