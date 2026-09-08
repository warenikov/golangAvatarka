package imageproc

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const jpegQuality = 85

// ThumbnailMIME — формат, в котором сохраняются миниатюры.
const ThumbnailMIME = "image/jpeg"

// Decode разбирает изображение, отказывая слишком большим по числу пикселей.
// Проверка идёт по заголовку, до распаковки: файл в сотню килобайт может
// развернуться в изображение на гигабайты памяти.
func Decode(data []byte, maxPixels int64) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}

	if pixels := int64(cfg.Width) * int64(cfg.Height); pixels > maxPixels {
		return nil, fmt.Errorf("изображение %dx%d превышает лимит в %d пикселей",
			cfg.Width, cfg.Height, maxPixels)
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}

	return img, nil
}

// Thumbnail строит квадратную миниатюру: сначала центральный кроп по короткой
// стороне, затем масштабирование. Непропорциональное растяжение для аватарки недопустимо.
func Thumbnail(src image.Image, size int) image.Image {
	b := src.Bounds()

	side := min(b.Dx(), b.Dy())
	offsetX := b.Min.X + (b.Dx()-side)/2
	offsetY := b.Min.Y + (b.Dy()-side)/2
	square := image.Rect(offsetX, offsetY, offsetX+side, offsetY+side)

	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, square, draw.Src, nil)

	return dst
}

// EncodeJPEG сохраняет изображение в JPEG.
func EncodeJPEG(w io.Writer, img image.Image) error {
	if err := jpeg.Encode(w, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return fmt.Errorf("encode jpeg: %w", err)
	}

	return nil
}

// Dimensions возвращает ширину и высоту изображения.
func Dimensions(img image.Image) (int, int) {
	b := img.Bounds()

	return b.Dx(), b.Dy()
}
