//go:build !dot && !spot

package display

import (
	"bytes"
	"container/list"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// bandsPNG is a w by h picture in three bands, red above green above blue, each a third of its height.
func bandsPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i, c := range []color.RGBA{{255, 0, 0, 255}, {0, 255, 0, 255}, {0, 0, 255, 255}} {
		draw.Draw(img, image.Rect(0, i*h/3, w, (i+1)*h/3), &image.Uniform{c}, image.Point{}, draw.Src)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A picture fitted to a box is cropped from its middle, not squeezed: a tall picture in a wide box shows
// the middle of it, which is all one band, where squeezing would show all three.
func TestPictureFitKeepsAspect(t *testing.T) {
	out, err := fitPicture(bandsPNG(t, 200, 600), 160, 90, mealsPicMaxPixels)
	if err != nil {
		t.Fatal(err)
	}
	if out.Rect.Dx() != 160 || out.Rect.Dy() != 90 {
		t.Fatalf("fitted to %v, want 160x90", out.Rect)
	}
	for _, y := range []int{10, 45, 80} {
		if c := out.RGBAAt(80, y); c.G < 240 || c.R > 15 || c.B > 15 {
			t.Fatalf("row %d is %v, want the middle band, green, all through", y, c)
		}
	}
	// The corners are rounded: the very corner shows what is behind the picture.
	if c := out.RGBAAt(0, 0); c.A != 0 {
		t.Fatalf("corner is %v, want it clear", c)
	}
	// A wide picture in a narrow box is cropped at its sides.
	wide := image.NewRGBA(image.Rect(0, 0, 300, 100))
	draw.Draw(wide, image.Rect(0, 0, 100, 100), &image.Uniform{color.RGBA{255, 0, 0, 255}}, image.Point{}, draw.Src)
	draw.Draw(wide, image.Rect(100, 0, 200, 100), &image.Uniform{color.RGBA{0, 255, 0, 255}}, image.Point{}, draw.Src)
	draw.Draw(wide, image.Rect(200, 0, 300, 100), &image.Uniform{color.RGBA{0, 0, 255, 255}}, image.Point{}, draw.Src)
	var buf bytes.Buffer
	png.Encode(&buf, wide)
	out, err = fitPicture(buf.Bytes(), 50, 100, mealsPicMaxPixels)
	if err != nil {
		t.Fatal(err)
	}
	if c := out.RGBAAt(25, 50); c.G < 240 {
		t.Fatalf("the middle of the wide picture is %v, want green", c)
	}
	if c := out.RGBAAt(2, 50); c.G < 240 || c.R > 15 {
		t.Fatalf("the left of the narrow box is %v, want the middle band's edge, green", c)
	}
}

// A picture above the limit is refused before it is decoded, and the original's limit is the larger.
func TestPictureFitRefusesOversize(t *testing.T) {
	big := bandsPNG(t, 2560, 1440) // 3.7 million pixels: over the small limit, under the original's
	if _, err := fitPicture(big, 960, 540, mealsPicMaxPixels); !errors.Is(err, errLibPicLarge) {
		t.Fatalf("a picture over the limit: %v, want it refused", err)
	}
	if _, err := fitPicture(big, 960, 540, mealsPicOriginalMaxPixels); err != nil {
		t.Fatalf("a picture under the original's limit: %v", err)
	}
	huge := bandsPNG(t, 3000, 2000) // 6 million: over both
	if _, err := fitPicture(huge, 960, 540, mealsPicOriginalMaxPixels); !errors.Is(err, errLibPicLarge) {
		t.Fatalf("a picture over the original's limit: %v, want it refused", err)
	}
	if _, err := fitPicture([]byte("not a picture"), 10, 10, mealsPicMaxPixels); err == nil {
		t.Fatal("garbage was fitted")
	}
}

// The three sizes a recipe's photo comes in (made-up stand-ins of about the sizes a server makes) fit
// their boxes exactly, are kept in the shared set under keys of their own beside a square cover of the
// same address, and the small limit is the one for the first two.
func TestPictureFitDecodedSize(t *testing.T) {
	sizes := []struct {
		name         string
		w, h         int // the variant as served
		boxW, boxH   int
		most         int
		wantRefusedA bool // refused at the small limit
	}{
		{"tiny", 250, 140, 72, 72, mealsPicMaxPixels, false},
		{"min", 640, 360, 370, 208, mealsPicMaxPixels, false},
		{"original", 2560, 1440, 345 * 2, 194 * 2, mealsPicOriginalMaxPixels, true},
	}
	for _, c := range sizes {
		b := bandsPNG(t, c.w, c.h)
		out, err := fitPicture(b, c.boxW, c.boxH, c.most)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if out.Rect.Dx() != c.boxW || out.Rect.Dy() != c.boxH {
			t.Fatalf("%s: fitted to %v, want %dx%d", c.name, out.Rect, c.boxW, c.boxH)
		}
		_, err = fitPicture(b, c.boxW, c.boxH, mealsPicMaxPixels)
		if (err != nil) != c.wantRefusedA {
			t.Fatalf("%s: at the small limit: %v, want refused %v", c.name, err, c.wantRefusedA)
		}
	}

	prev := libFetch
	t.Cleanup(func() { libFetch = prev })
	pic := bandsPNG(t, 640, 360)
	libFetch = func(context.Context, string) ([]byte, int, error) { return pic, http.StatusOK, nil }
	p := &libPictures{}
	got := make(chan struct{}, 4)
	p.setChanged(func() { got <- struct{}{} })
	const u = "http://example.test/cover.webp"
	if p.pictureFit(u, 370, 208) != nil {
		t.Fatal("a picture was there before it was fetched")
	}
	if p.picture(u, 64, false) != nil {
		t.Fatal("a square was there before it was fetched")
	}
	for range 2 {
		select {
		case <-got:
		case <-time.After(5 * time.Second):
			t.Fatal("the pictures did not arrive")
		}
	}
	fit, sq := p.pictureFit(u, 370, 208), p.picture(u, 64, false)
	if fit == nil || fit.Rect.Dx() != 370 || fit.Rect.Dy() != 208 {
		t.Fatalf("fitted picture %v", fit)
	}
	if sq == nil || sq.Rect.Dx() != 64 || sq.Rect.Dy() != 64 {
		t.Fatalf("square picture %v: the fitter changed the square path", sq)
	}
	p.mu.Lock()
	_, other := p.kept[libPicFitKey(u, 72, 72)]
	p.mu.Unlock()
	if other {
		t.Fatal("a box of another size shared a key")
	}
	if len(p.kept) != 2 {
		t.Fatalf("%d pictures kept, want the fitted one and the square one", len(p.kept))
	}
}

// A store that has seen many pictures over the size limit does not remember them all: the set is
// bounded, and a picture that fell out of it is only asked for once more.
func TestPictureLargeSetIsBounded(t *testing.T) {
	p := &libPictures{asked: map[string]bool{}, failed: map[string]time.Time{}, kept: map[string]*list.Element{},
		slots: make(chan struct{}, 1)}
	for i := range 3 * libPicsLargeMost {
		p.fetch(fmt.Sprintf("big %d", i), "https://example.test/big", func() (*image.RGBA, error) { return nil, errLibPicLarge })
	}
	if n := len(p.large); n == 0 || n > libPicsLargeMost {
		t.Fatalf("%d pictures remembered as too large, want between 1 and %d", n, libPicsLargeMost)
	}
}

// pictureFitKept hands back a picture that is kept and never asks for one that is not.
func TestPictureFitKeptDoesNotAsk(t *testing.T) {
	var asked atomic.Int32
	was := libFetch
	libFetch = func(context.Context, string) ([]byte, int, error) {
		asked.Add(1)
		return bandsPNG(t, 160, 90), http.StatusOK, nil
	}
	t.Cleanup(func() { libFetch = was })
	p := &libPictures{}
	if p.pictureFitKept("https://example.test/a.png", 80, 45) != nil || asked.Load() != 0 {
		t.Fatalf("a picture that was never asked for: asked %d", asked.Load())
	}
	for i := 0; p.pictureFit("https://example.test/a.png", 80, 45) == nil; i++ {
		if i > 500 {
			t.Fatal("the picture never came")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if p.pictureFitKept("https://example.test/a.png", 80, 45) == nil {
		t.Fatal("a kept picture was not handed back")
	}
	if p.pictureFitKept("https://example.test/a.png", 81, 45) != nil || asked.Load() != 1 {
		t.Fatalf("another size: asked %d times", asked.Load())
	}
}
