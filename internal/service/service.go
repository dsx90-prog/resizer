package service

import (
	"fmt"
	"image"
	"io"
	"log/slog"
	"net/http"
	"os"
	imagep "resizer/pkg/image"
	"time"
)

func CheckField(newFilePath string) bool {
	_, err := os.Stat(newFilePath)
	if err == nil {
		return true
	} else if os.IsNotExist(err) {
		return false
	} else {
		slog.Error("Error checking file", "path", newFilePath, "error", err)
	}

	return false
}

func DownloadImage(fileName, imageURL string) (image.Image, error) {
	client := &http.Client{
		Timeout: 15 * time.Second, // Жесткий таймаут для всех операций с удаленным сервером
	}
	req, err := http.NewRequest("GET", imageURL, nil)
	if err != nil {
		return nil, err
	}

	// Добавляем современный User-Agent, чтобы избежать блокировок (ошибка 403)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download image: status code %d", response.StatusCode)
	}

	// Ограничиваем размер скачиваемого файла до 15 МБ для защиты от OOM
	const maxFileSize = 15 * 1024 * 1024 // 15 MB
	limitedBody := io.LimitReader(response.Body, maxFileSize)

	return imagep.DecodeImage(fileName, limitedBody)
}

func CreateMeta(dir string, updateData string) {
	_ = CreateDirectory(dir)
	// Создаем файл
	file, err := os.Create(fmt.Sprintf("%s/%s", dir, ".meta"))
	if err != nil {
		slog.Error("Error creating meta file", "dir", dir, "error", err)
		return
	}
	defer file.Close()

	if updateData != "" {
		_, err = file.Write([]byte(fmt.Sprintf("lastDate: %s\n", updateData)))
		if err != nil {
			slog.Error("Error writing to meta file", "error", err)
			return
		}
	}
}

func Save(dir, newFilePath string) (*os.File, error) {
	if err := CreateDirectory(dir); err != nil {
		return nil, err
	}
	// Сохранить изображение
	outputFile, err := os.Create(newFilePath)
	if err != nil {
		return nil, err
	}

	return outputFile, nil
}

func CreateDirectory(dir string) error {
	// Создать папки
	err := os.MkdirAll(dir, os.ModePerm)
	if err != nil {
		slog.Error("Error creating directory", "path", dir, "error", err)
		return err
	}
	return nil
}
