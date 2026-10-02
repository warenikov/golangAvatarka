package form_test

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/handlers/form"
)

var allowedMIME = []string{"image/jpeg", "image/png", "image/webp"}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 70, A: 255})
		}
	}

	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))

	return buf.Bytes()
}

func request(t *testing.T, field, filename string, content []byte) *http.Request {
	t.Helper()

	var body bytes.Buffer
	form := multipart.NewWriter(&body)

	if field != "" {
		part, err := form.CreateFormFile(field, filename)
		require.NoError(t, err)
		_, err = part.Write(content)
		require.NoError(t, err)
	}

	require.NoError(t, form.WriteField("user_id", "user-1"))
	require.NoError(t, form.Close())

	req := httptest.NewRequest(http.MethodPost, "/upload", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())

	return req
}

// Готовый фронтенд курса шлёт файл в поле image, документация к API — в file.
func TestReadImageAcceptsBothFieldNames(t *testing.T) {
	tests := []string{form.FieldFile, form.FieldImage}

	for _, field := range tests {
		t.Run(field, func(t *testing.T) {
			content := pngBytes(t, 40, 30)

			img, err := form.ReadImage(request(t, field, "avatar.png", content), allowedMIME)
			require.NoError(t, err)
			defer func() { _ = img.Close() }()

			assert.Equal(t, "avatar.png", img.Name)
			assert.Equal(t, "image/png", img.MIME)
			assert.Equal(t, int64(len(content)), img.Size)

			// Байты, прочитанные при определении формата, должны остаться доступными.
			got, err := io.ReadAll(img.Body)
			require.NoError(t, err)
			assert.Equal(t, content, got, "тело должно отдаваться целиком")
		})
	}
}

func TestReadImageErrors(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		content []byte
		wantErr error
	}{
		{
			name:    "файла нет ни в одном поле",
			wantErr: http.ErrMissingFile,
		},
		{
			name:    "поле с другим именем",
			field:   "picture",
			content: pngBytes(t, 10, 10),
			wantErr: http.ErrMissingFile,
		},
		{
			name:    "текст вместо картинки",
			field:   form.FieldFile,
			content: []byte(strings.Repeat("это просто текст. ", 40)),
			wantErr: domain.ErrInvalidFormat,
		},
		{
			name:    "gif не в белом списке",
			field:   form.FieldFile,
			content: append([]byte("GIF89a"), bytes.Repeat([]byte{0}, 600)...),
			wantErr: domain.ErrInvalidFormat,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img, err := form.ReadImage(request(t, tt.field, "f.bin", tt.content), allowedMIME)

			require.ErrorIs(t, err, tt.wantErr)
			assert.Nil(t, img)
		})
	}
}

// Формат берётся из содержимого: имя файла и заголовок задаёт клиент.
func TestReadImageIgnoresClaimedNameAndType(t *testing.T) {
	content := pngBytes(t, 20, 20)

	img, err := form.ReadImage(request(t, form.FieldFile, "malware.exe", content), allowedMIME)
	require.NoError(t, err)
	defer func() { _ = img.Close() }()

	assert.Equal(t, "image/png", img.MIME, "тип определяется по сигнатуре, а не по расширению")
	assert.Equal(t, "malware.exe", img.Name, "имя сохраняется как есть — оно только для показа")
}

func TestImageCloseIsSafe(t *testing.T) {
	img, err := form.ReadImage(request(t, form.FieldFile, "a.png", pngBytes(t, 10, 10)), allowedMIME)
	require.NoError(t, err)

	require.NoError(t, img.Close())
}

func TestReadImageCleansFileName(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     string
	}{
		{"обычное имя не меняется", "аватар.png", "аватар.png"},
		{"длинное имя обрезается по символам, а не по байтам", strings.Repeat("я", 300) + ".png", strings.Repeat("я", 255)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img, err := form.ReadImage(request(t, form.FieldFile, tt.filename, pngBytes(t, 10, 10)), allowedMIME)
			require.NoError(t, err)
			defer func() { _ = img.Close() }()

			assert.Equal(t, tt.want, img.Name)
		})
	}
}

// Сырые управляющие символы multipart не пропускает, а закодированные по RFC 2231 — да.
func TestReadImageDropsEncodedControlChars(t *testing.T) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="file"; filename*=UTF-8''ava%00tar%01.png`)
	header.Set("Content-Type", "application/octet-stream")

	part, err := mw.CreatePart(header)
	require.NoError(t, err)
	_, err = part.Write(pngBytes(t, 10, 10))
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	req := httptest.NewRequest(http.MethodPost, "/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	img, err := form.ReadImage(req, allowedMIME)
	require.NoError(t, err)
	defer func() { _ = img.Close() }()

	assert.Equal(t, "avatar.png", img.Name, "NUL не принимает PostgreSQL, и строка не записалась бы")
}
