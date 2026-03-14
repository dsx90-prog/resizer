package service

import (
	"fmt"
	"image"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	imagep "resizer/pkg/image"
	"strings"
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
	resp, err := downloadRequest(imageURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Ограничиваем размер скачиваемого файла до 15 МБ для защиты от OOM
	const maxFileSize = 15 * 1024 * 1024 // 15 MB
	limitedBody := io.LimitReader(resp.Body, maxFileSize)

	return imagep.DecodeImage(fileName, limitedBody)
}

// DownloadMeta contains filepath and mime type
type DownloadMeta struct {
	FilePath    string
	ContentType string // e.g: "video/mp4", "image/jpeg"
	IsVideo     bool
}

// DownloadToDisk downloads arbitrary file to a given directory, returning metadata
func DownloadToDisk(imageURL string, destDir string) (*DownloadMeta, error) {
	resp, err := downloadRequest(imageURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")
	contentType, _, _ = mime.ParseMediaType(contentType)

	isVideo := strings.HasPrefix(contentType, "video/")
	var maxFileSize int64
	if isVideo {
		maxFileSize = 200 * 1024 * 1024 // 200 MB limit for videos
	} else {
		maxFileSize = 25 * 1024 * 1024 // 25 MB limit for anything else
	}

	tempFile, err := os.CreateTemp(destDir, "download_*.tmp")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	tempFileName := tempFile.Name()

	limitedBody := io.LimitReader(resp.Body, maxFileSize)
	written, err := io.Copy(tempFile, limitedBody)
	tempFile.Close() // Close before possible error return

	if err != nil {
		os.Remove(tempFileName)
		return nil, fmt.Errorf("failed to download file: %w", err)
	}

	if written == maxFileSize {
		os.Remove(tempFileName)
		return nil, fmt.Errorf("file exceeded size limit format (Video: 200MB, Other: 25MB)")
	}

	return &DownloadMeta{
		FilePath:    tempFileName,
		ContentType: contentType,
		IsVideo:     isVideo,
	}, nil
}

func DownloadStream(imageURL string) (*http.Response, error) {
	return downloadRequest(imageURL)
}

func downloadRequest(imageURL string) (*http.Response, error) {
	client := &http.Client{
		Timeout: 30 * time.Second, // Timeout для всех операций с удаленным сервером
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

	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("failed to download: status code %d", response.StatusCode)
	}

	return response, nil
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
