package application

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/XManCodeing/XMall/internal/i18n"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// TestRejectionFeedbackSharedLocales checks all rejection reasons against shared
// catalogue entries and native language markers; t reports missing translations.
func TestRejectionFeedbackSharedLocales(t *testing.T) {
	for _, locale := range []struct{ input, canonical, marker string }{
		{"zh-CN", "zh-CN", "未保存或送达"},
		{"zh-TW", "zh-TW", "未儲存或送達"},
		{"zh-Hant", "zh-TW", "未儲存或送達"},
		{"en", "en-US", "not saved or delivered"},
		{"en-US", "en-US", "not saved or delivered"},
	} {
		for _, reason := range []string{"album_late_or_limit", "content_protected", "content_unsupported"} {
			t.Run(locale.input+"/"+reason, func(t *testing.T) {
				got := feedback(locale.input, reason)
				if got != i18n.T(locale.canonical, "support."+reason) || !strings.Contains(got, locale.marker) {
					t.Fatalf("missing shared locale feedback: %q", got)
				}
			})
		}
	}
}

// TestArithmeticPNGCentered checks decoded pixels, safe margins and complete glyphs.
// t reports failures for every supported operator, narrow/wide numbers and fit boundaries.
func TestArithmeticPNGCentered(t *testing.T) {
	for _, question := range []string{"1 + 12 = ?", "22 / 2 = ?", "19 * 19 = ?", "361 / 19 = ?", "1 - 1 = ?", "19 + 19 = ?", "18 - 9 = ?", "100000000000000000000000000000000000 = ?"} {
		t.Run(question, func(t *testing.T) {
			encoded, err := arithmeticPNG(question)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := png.Decode(bytes.NewReader(encoded))
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Bounds() != image.Rect(0, 0, 320, 100) {
				t.Fatal("unexpected canvas", decoded.Bounds())
			}
			bounds, _ := font.BoundString(basicfont.Face7x13, question)
			width, height := bounds.Max.X.Ceil()-bounds.Min.X.Floor(), bounds.Max.Y.Ceil()-bounds.Min.Y.Floor()
			scale := min(3, 280/width, 60/height)
			// Render independently into an oversized canvas to detect any lost ink.
			reference := image.NewRGBA(image.Rect(0, 0, 600, 100))
			painter := font.Drawer{Dst: reference, Src: image.NewUniform(color.Black), Face: basicfont.Face7x13, Dot: fixed.P(10, 40)}
			painter.DrawString(question)
			ink := image.Rectangle{}
			actualPixels, referencePixels := 0, 0
			for vertical := 0; vertical < 100; vertical++ {
				for horizontal := 0; horizontal < 600; horizontal++ {
					if reference.RGBAAt(horizontal, vertical).A != 0 {
						referencePixels++
					}
					if horizontal >= 320 {
						continue
					}
					pixel := color.RGBAModel.Convert(decoded.At(horizontal, vertical)).(color.RGBA)
					if pixel == (color.RGBA{30, 42, 55, 255}) {
						actualPixels++
						ink = ink.Union(image.Rect(horizontal, vertical, horizontal+1, vertical+1))
					}
				}
			}
			if actualPixels != referencePixels*scale*scale || ink.Empty() {
				t.Fatal("glyph pixels were clipped", actualPixels, referencePixels, scale)
			}
			if ink.Min.X < 20 || ink.Max.X > 300 || ink.Min.Y < 20 || ink.Max.Y > 80 {
				t.Fatal("glyphs escaped safe area", ink)
			}
			if difference := ink.Min.X - (320 - ink.Max.X); difference < -3 || difference > 3 {
				t.Fatal("horizontal centering incorrect", ink)
			}
			if difference := ink.Min.Y - (100 - ink.Max.Y); difference < -3 || difference > 3 {
				t.Fatal("vertical centering incorrect", ink)
			}
			again, err := arithmeticPNG(question)
			if err != nil || !bytes.Equal(encoded, again) {
				t.Fatal("render is not deterministic", err)
			}
		})
	}
	for _, invalid := range []string{"", "                                                                ", "12345678901234567890123456789012345678901234567890123456789012345"} {
		if _, err := arithmeticPNG(invalid); err == nil {
			t.Fatal("invalid or oversized question accepted")
		}
	}
}
