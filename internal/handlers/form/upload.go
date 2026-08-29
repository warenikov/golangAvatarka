// Package form разбирает загрузку изображения из HTTP-формы.
//
// Одна и та же операция доступна и через REST API, и через веб-форму, а разбор
// multipart, определение формата и проверка по списку разрешённых у них общие.
// В пакетах-обработчиках остаётся только представление ошибки: JSON у API,
// страница у веб-интерфейса.
package form

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"

	"go-avatar-service/internal/services/imageproc"
)

// Имена полей формы: готовый фронтенд курса шлёт файл в image, документация
// к API — в file. Принимаются оба.
const (
	FieldFile  = "file"
	FieldImage = "image"
)

// Image — загруженное изображение с уже определённым форматом.
// Close обязателен к вызову.
type Image struct {
	// Name — имя файла, как его назвал клиент. Значение не доверенное:
	// годится для показа и хранения, но не для построения путей.
	Name string
	Size int64
	MIME string
	// Body отдаёт файл целиком, включая байты, прочитанные при определении формата.
	Body io.Reader

	file multipart.File
}

// Close освобождает временный файл, созданный разбором multipart.
func (i *Image) Close() error {
	if i.file == nil {
		return nil
	}

	if err := i.file.Close(); err != nil {
		return fmt.Errorf("close uploaded file: %w", err)
	}

	return nil
}

// ReadImage достаёт файл из формы, определяет формат по сигнатуре и проверяет
// его по списку разрешённых.
//
// Формат берётся из содержимого, а не из имени файла или Content-Type: и то,
// и другое задаёт клиент.
func ReadImage(r *http.Request, allowedMIME []string) (*Image, error) {
	file, header, err := openUploadedFile(r)
	if err != nil {
		return nil, err
	}

	mime, body, err := imageproc.Detect(file)
	if err != nil {
		_ = file.Close()

		return nil, err
	}

	if err = imageproc.ValidateMIME(mime, allowedMIME); err != nil {
		_ = file.Close()

		return nil, err
	}

	return &Image{
		Name: header.Filename,
		Size: header.Size,
		MIME: mime,
		Body: body,
		file: file,
	}, nil
}

func openUploadedFile(r *http.Request) (multipart.File, *multipart.FileHeader, error) {
	file, header, err := r.FormFile(FieldFile)
	if err == nil {
		return file, header, nil
	}

	// Поле file отсутствует — пробуем image. Прочие ошибки разбора формы
	// возвращаются как есть: их обрабатывает вызывающий.
	if !isMissingFile(err) {
		return nil, nil, err
	}

	file, header, err = r.FormFile(FieldImage)
	if err != nil {
		return nil, nil, err
	}

	return file, header, nil
}

func isMissingFile(err error) bool {
	return errors.Is(err, http.ErrMissingFile)
}
