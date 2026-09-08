package imageproc_test

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/services/imageproc"
)

const maxTestPixels = 1 << 24

var allowedMIME = []string{"image/jpeg", "image/png", "image/webp"}

// solidImage рисует прямоугольник одного цвета.
func solidImage(w, h int, c color.Color) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}

	return img
}

// stripedImage рисует прямоугольник, у которого центральный квадрат по короткой
// стороне залит inner, а всё остальное — outer. На таком изображении видно,
// действительно ли кроп берёт центр.
func stripedImage(w, h int, inner, outer color.Color) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	side := min(w, h)
	x0, y0 := (w-side)/2, (h-side)/2

	for y := range h {
		for x := range w {
			if x >= x0 && x < x0+side && y >= y0 && y < y0+side {
				img.Set(x, y, inner)
			} else {
				img.Set(x, y, outer)
			}
		}
	}

	return img
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()

	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))

	return buf.Bytes()
}

func encodeJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()

	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))

	return buf.Bytes()
}

func readWebP(t *testing.T) []byte {
	t.Helper()

	data, err := os.ReadFile("testdata/sample.webp")
	require.NoError(t, err)

	return data
}

func TestDetect(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"png", encodePNG(t, solidImage(20, 20, color.White)), "image/png"},
		{"jpeg", encodeJPEG(t, solidImage(20, 20, color.White)), "image/jpeg"},
		{"webp", readWebP(t), "image/webp"},
		{"текст вместо картинки", []byte("это не картинка, а просто текст"), "text/plain; charset=utf-8"},
		{"пустое тело", []byte{}, "text/plain; charset=utf-8"},
		{"больше буфера сниффинга", encodePNG(t, solidImage(200, 200, color.Black)), "image/png"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mime, body, err := imageproc.Detect(bytes.NewReader(tt.data))
			require.NoError(t, err)
			assert.Equal(t, tt.want, mime)

			// Прочитанное начало должно остаться доступным следующему читателю.
			rest, err := io.ReadAll(body)
			require.NoError(t, err)
			assert.Equal(t, tt.data, rest, "содержимое после Detect должно совпадать с исходным")
		})
	}
}

func TestDetectReadError(t *testing.T) {
	_, _, err := imageproc.Detect(iotest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read head")
}

// iotest — читатель, который всегда падает.
type iotest struct{}

func (iotest) Read([]byte) (int, error) { return 0, assert.AnError }

func TestValidateMIME(t *testing.T) {
	tests := []struct {
		name    string
		mime    string
		wantErr bool
	}{
		{"jpeg разрешён", "image/jpeg", false},
		{"png разрешён", "image/png", false},
		{"webp разрешён", "image/webp", false},
		{"gif запрещён", "image/gif", true},
		{"текст запрещён", "text/plain; charset=utf-8", true},
		{"пустой тип запрещён", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := imageproc.ValidateMIME(tt.mime, allowedMIME)
			if !tt.wantErr {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, domain.ErrInvalidFormat)
			assert.Contains(t, err.Error(), tt.mime)
		})
	}
}

func TestDecode(t *testing.T) {
	tests := []struct {
		name      string
		data      []byte
		maxPixels int64
		wantW     int
		wantH     int
		wantErr   string
	}{
		{name: "png", data: encodePNG(t, solidImage(40, 25, color.White)), maxPixels: maxTestPixels, wantW: 40, wantH: 25},
		{name: "jpeg", data: encodeJPEG(t, solidImage(30, 30, color.Black)), maxPixels: maxTestPixels, wantW: 30, wantH: 30},
		{name: "webp", data: readWebP(t), maxPixels: maxTestPixels, wantW: 60, wantH: 40},
		{name: "битые байты", data: []byte("совсем не картинка"), maxPixels: maxTestPixels, wantErr: "decode config"},
		{name: "обрезанный png", data: encodePNG(t, solidImage(40, 40, color.White))[:20], maxPixels: maxTestPixels, wantErr: "decode config"},
		{name: "слишком много пикселей", data: encodePNG(t, solidImage(40, 40, color.White)), maxPixels: 100, wantErr: "превышает лимит"},
		{name: "ровно на границе лимита", data: encodePNG(t, solidImage(10, 10, color.White)), maxPixels: 100, wantW: 10, wantH: 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img, err := imageproc.Decode(tt.data, tt.maxPixels)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Nil(t, img)

				return
			}

			require.NoError(t, err)
			w, h := imageproc.Dimensions(img)
			assert.Equal(t, tt.wantW, w)
			assert.Equal(t, tt.wantH, h)
		})
	}
}

func TestThumbnailIsSquare(t *testing.T) {
	tests := []struct {
		name string
		src  image.Image
		size int
	}{
		{"широкое", solidImage(400, 200, color.White), 100},
		{"высокое", solidImage(200, 400, color.White), 100},
		{"квадратное", solidImage(300, 300, color.White), 300},
		{"меньше запрошенного размера", solidImage(50, 40, color.White), 300},
		{"один пиксель", solidImage(1, 1, color.White), 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			thumb := imageproc.Thumbnail(tt.src, tt.size)

			w, h := imageproc.Dimensions(thumb)
			assert.Equal(t, tt.size, w)
			assert.Equal(t, tt.size, h)
		})
	}
}

func TestThumbnailCropsCenter(t *testing.T) {
	inner := color.RGBA{R: 0, G: 0, B: 255, A: 255}
	outer := color.RGBA{R: 255, G: 0, B: 0, A: 255}

	tests := []struct {
		name string
		src  image.Image
	}{
		{"широкое", stripedImage(400, 200, inner, outer)},
		{"высокое", stripedImage(200, 400, inner, outer)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			thumb := imageproc.Thumbnail(tt.src, 100)

			// В кадр должен попасть только центральный квадрат: краёв другого цвета нет.
			for _, p := range []image.Point{{X: 1, Y: 1}, {X: 98, Y: 1}, {X: 1, Y: 98}, {X: 98, Y: 98}, {X: 50, Y: 50}} {
				r, _, b, _ := thumb.At(p.X, p.Y).RGBA()
				assert.Greater(t, b, r, "пиксель (%d,%d) должен быть из центра исходного изображения", p.X, p.Y)
			}
		})
	}
}

func TestEncodeJPEG(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, imageproc.EncodeJPEG(&buf, imageproc.Thumbnail(solidImage(200, 120, color.White), 100)))

	mime, _, err := imageproc.Detect(bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
	assert.Equal(t, imageproc.ThumbnailMIME, mime)

	decoded, err := imageproc.Decode(buf.Bytes(), maxTestPixels)
	require.NoError(t, err)

	w, h := imageproc.Dimensions(decoded)
	assert.Equal(t, 100, w)
	assert.Equal(t, 100, h)
}

func TestEncodeJPEGWriteError(t *testing.T) {
	err := imageproc.EncodeJPEG(failingWriter{}, solidImage(10, 10, color.White))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "encode jpeg")
}

// failingWriter — writer, который всегда падает.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, assert.AnError }

func TestDetectShortInput(t *testing.T) {
	data := []byte(strings.Repeat("a", 10))

	mime, body, err := imageproc.Detect(bytes.NewReader(data))
	require.NoError(t, err)
	assert.Equal(t, "text/plain; charset=utf-8", mime)

	rest, err := io.ReadAll(body)
	require.NoError(t, err)
	assert.Equal(t, data, rest)
}
