package media

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"strings"
)

func init() {
	image.RegisterFormat("png", "PNG", png.Decode, png.DecodeConfig)
	image.RegisterFormat("jpeg", "JPEG", jpeg.Decode, jpeg.DecodeConfig)
	image.RegisterFormat("gif", "GIF", gif.Decode, gif.DecodeConfig)
}

type Dimensions struct {
	Width  int
	Height int
	Format string
}

func DetectDimensions(data []byte) (Dimensions, bool) {
	if len(data) < 10 {
		return Dimensions{}, false
	}
	if len(data) >= 24 && data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' {
		w := int(binary.BigEndian.Uint32(data[16:20]))
		h := int(binary.BigEndian.Uint32(data[20:24]))
		if w > 0 && h > 0 {
			return Dimensions{Width: w, Height: h, Format: "png"}, true
		}
		return Dimensions{}, false
	}
	if data[0] == 0xFF && data[1] == 0xD8 {
		if w, h, ok := jpegDimensions(data); ok {
			return Dimensions{Width: w, Height: h, Format: "jpeg"}, true
		}
		return Dimensions{}, false
	}
	if len(data) >= 10 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a") {
		w := int(binary.LittleEndian.Uint16(data[6:8]))
		h := int(binary.LittleEndian.Uint16(data[8:10]))
		if w > 0 && h > 0 {
			return Dimensions{Width: w, Height: h, Format: "gif"}, true
		}
		return Dimensions{}, false
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		if w, h, ok := webpDimensions(data); ok {
			return Dimensions{Width: w, Height: h, Format: "webp"}, true
		}
		return Dimensions{}, false
	}
	if len(data) >= 26 && data[0] == 'B' && data[1] == 'M' {
		w := int(int32(binary.LittleEndian.Uint32(data[18:22])))
		h := int(int32(binary.LittleEndian.Uint32(data[22:26])))
		if h < 0 {
			h = -h
		}
		if w > 0 && h > 0 {
			return Dimensions{Width: w, Height: h, Format: "bmp"}, true
		}
	}
	return Dimensions{}, false
}

func jpegDimensions(data []byte) (int, int, bool) {
	pos := 2
	for pos+4 < len(data) {
		if data[pos] != 0xFF {
			pos++
			continue
		}
		marker := data[pos+1]
		if marker == 0xD8 || marker == 0xD9 || (marker >= 0xD0 && marker <= 0xD7) || marker == 0x01 {
			pos += 2
			continue
		}
		if pos+4 > len(data) {
			return 0, 0, false
		}
		segLen := int(binary.BigEndian.Uint16(data[pos+2 : pos+4]))
		if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
			if pos+9 > len(data) {
				return 0, 0, false
			}
			h := int(binary.BigEndian.Uint16(data[pos+5 : pos+7]))
			w := int(binary.BigEndian.Uint16(data[pos+7 : pos+9]))
			if w > 0 && h > 0 {
				return w, h, true
			}
			return 0, 0, false
		}
		pos += 2 + segLen
	}
	return 0, 0, false
}

func webpDimensions(data []byte) (int, int, bool) {
	if len(data) < 30 {
		return 0, 0, false
	}
	fourcc := string(data[12:16])
	switch fourcc {
	case "VP8 ":
		if len(data) < 30 {
			return 0, 0, false
		}
		w := int(data[26]) | int(data[27])<<8
		h := int(data[28]) | int(data[29])<<8
		if w > 0 && h > 0 {
			return w, h, true
		}
	case "VP8L":
		if len(data) < 25 {
			return 0, 0, false
		}
		b0 := int(data[21])
		b1 := int(data[22])
		b2 := int(data[23])
		b3 := int(data[24])
		w := 1 + (((b1 & 0x3F) << 8) | b0)
		h := 1 + (((b3 & 0x0F) << 10) | (b2 << 2) | ((b1 & 0xC0) >> 6))
		if w > 0 && h > 0 {
			return w, h, true
		}
	case "VP8X":
		if len(data) < 30 {
			return 0, 0, false
		}
		w := 1 + (int(data[24]) | int(data[25])<<8 | int(data[26])<<16)
		h := 1 + (int(data[27]) | int(data[28])<<8 | int(data[29])<<16)
		if w > 0 && h > 0 {
			return w, h, true
		}
	}
	return 0, 0, false
}

func IsAnimatedAPNG(data []byte) bool {
	end := len(data)
	if end > 200 {
		end = 200
	}
	return bytes.Contains(data[:end], []byte("acTL"))
}

func IsAnimatedWebP(data []byte) bool {
	end := len(data)
	if end > 200 {
		end = 200
	}
	head := data[:end]
	return bytes.Contains(head, []byte("ANIM")) || bytes.Contains(head, []byte("ANMF"))
}

func DecodeImage(data []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

func AverageColorHex(img image.Image) string {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return "#808080"
	}
	var rSum, gSum, bSum, n uint64
	xStep := 1
	if w > 64 {
		xStep = w / 64
	}
	yStep := 1
	if h > 64 {
		yStep = h / 64
	}
	for y := b.Min.Y; y < b.Max.Y; y += yStep {
		for x := b.Min.X; x < b.Max.X; x += xStep {
			r, g, bl, _ := img.At(x, y).RGBA()
			rSum += uint64(r >> 8)
			gSum += uint64(g >> 8)
			bSum += uint64(bl >> 8)
			n++
		}
	}
	if n == 0 {
		return "#808080"
	}
	return fmt.Sprintf("#%02x%02x%02x", rSum/n, gSum/n, bSum/n)
}

func scaleTo(img image.Image, w, h int) image.Image {
	if w <= 0 || h <= 0 {
		return img
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	bilinearScale(dst, img)
	return dst
}

func bilinearScale(dst *image.RGBA, src image.Image) {
	sb := src.Bounds()
	dw, dh := dst.Bounds().Dx(), dst.Bounds().Dy()
	sw, sh := sb.Dx(), sb.Dy()
	if dw <= 0 || dh <= 0 || sw <= 0 || sh <= 0 {
		return
	}
	for y := 0; y < dh; y++ {
		fy := (float64(y)+0.5)*float64(sh)/float64(dh) - 0.5
		y0 := int(fy)
		wy := fy - float64(y0)
		if y0 < 0 {
			y0, wy = 0, 0
		}
		if y0 >= sh-1 {
			y0, wy = sh-2, 1
			if y0 < 0 {
				y0, wy = 0, 0
			}
		}
		for x := 0; x < dw; x++ {
			fx := (float64(x)+0.5)*float64(sw)/float64(dw) - 0.5
			x0 := int(fx)
			wx := fx - float64(x0)
			if x0 < 0 {
				x0, wx = 0, 0
			}
			if x0 >= sw-1 {
				x0, wx = sw-2, 1
				if x0 < 0 {
					x0, wx = 0, 0
				}
			}
			c00 := toNRGBA(src.At(sb.Min.X+x0, sb.Min.Y+y0))
			c10 := toNRGBA(src.At(sb.Min.X+x0+1, sb.Min.Y+y0))
			c01 := toNRGBA(src.At(sb.Min.X+x0, sb.Min.Y+y0+1))
			c11 := toNRGBA(src.At(sb.Min.X+x0+1, sb.Min.Y+y0+1))
			r := bilerp(c00.R, c10.R, c01.R, c11.R, wx, wy)
			g := bilerp(c00.G, c10.G, c01.G, c11.G, wx, wy)
			b := bilerp(c00.B, c10.B, c01.B, c11.B, wx, wy)
			a := bilerp(c00.A, c10.A, c01.A, c11.A, wx, wy)
			dst.SetRGBA(x, y, uint8Color(r, g, b, a))
		}
	}
}

type nrgbaF struct{ R, G, B, A float64 }

func toNRGBA(c interface {
	RGBA() (r, g, b, a uint32)
}) nrgbaF {
	r, g, b, a := c.RGBA()
	if a == 0 {
		return nrgbaF{}
	}
	scale := 255.0 / float64(a)
	return nrgbaF{
		R: float64(r) * scale,
		G: float64(g) * scale,
		B: float64(b) * scale,
		A: float64(a) * 255 / 65535,
	}
}

func bilerp(c00, c10, c01, c11, wx, wy float64) float64 {
	top := c00*(1-wx) + c10*wx
	bot := c01*(1-wx) + c11*wx
	return top*(1-wy) + bot*wy
}

func uint8Color(r, g, b, a float64) (c color.RGBA) {
	clamp := func(v float64) uint8 {
		if v < 0 {
			return 0
		}
		if v > 255 {
			return 255
		}
		return uint8(v + 0.5)
	}
	c.R, c.G, c.B, c.A = clamp(r), clamp(g), clamp(b), clamp(a)
	return c
}

func Cover(img image.Image, w, h int) image.Image {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return img
	}
	scale := float64(w) / float64(sw)
	if float64(sh)*scale < float64(h) {
		scale = float64(h) / float64(sh)
	}
	nw := int(float64(sw) * scale)
	nh := int(float64(sh) * scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	scaled := scaleTo(img, nw, nh)
	ox := (nw - w) / 2
	oy := (nh - h) / 2
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), scaled, image.Pt(ox, oy), draw.Src)
	return dst
}

func FitByArea(img image.Image, targetArea int) (image.Image, int, int) {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 || targetArea <= 0 {
		return img, sw, sh
	}
	ratio := float64(sw) / float64(sh)
	nw := int(math.Sqrt(float64(targetArea)*ratio) + 0.5)
	nh := int(math.Sqrt(float64(targetArea)/ratio) + 0.5)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	return scaleTo(img, nw, nh), nw, nh
}

func EncodeJPEG(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func EncodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func CropImage(img image.Image, x, y, w, h int) image.Image {
	b := img.Bounds()
	if x < b.Min.X {
		x = b.Min.X
	}
	if y < b.Min.Y {
		y = b.Min.Y
	}
	if x+w > b.Max.X {
		w = b.Max.X - x
	}
	if y+h > b.Max.Y {
		h = b.Max.Y - y
	}
	if w <= 0 || h <= 0 {
		return img
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), img, image.Pt(x, y), draw.Src)
	return dst
}

func IsImageFile(name string) bool {
	lower := strings.ToLower(name)
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tif", ".tiff", ".apng", ".jfif", ".avif", ".svg"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

func ReadImageFile(path string) (image.Image, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	img, err := DecodeImage(data)
	if err != nil {
		return nil, data, err
	}
	return img, data, nil
}
