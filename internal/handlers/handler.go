package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image/png"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"resizer/internal/service"
	imagep "resizer/pkg/image"
	"strconv"
	"strings"
	"time"

	"github.com/gen2brain/webp"
)

var AllowedDomains []string
var StoragePath string = "artefacts" // Default value

func getFileHash(filePath string) string {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func ResizeHandler(w http.ResponseWriter, r *http.Request) {
	// Получить параметры из URL
	params := r.URL.Query()
	rawImageURL := params.Get("url")
	if rawImageURL == "" {
		http.Error(w, "Parameter 'url' is required", http.StatusBadRequest)
		return
	}

	width, _ := strconv.Atoi(params.Get("width"))
	height, _ := strconv.Atoi(params.Get("height"))
	radius, _ := strconv.Atoi(params.Get("radius"))

	cropX := params.Get("crop_x")
	if cropX == "" {
		cropX = "center"
	}
	cropY := params.Get("crop_y")
	if cropY == "" {
		cropY = "center"
	}

	format := params.Get("format")
	if format == "" {
		format = "png"
	} else {
		format = strings.ToLower(format)
	}

	qualityStr := params.Get("q")
	if qualityStr == "" {
		qualityStr = params.Get("quality")
	}
	quality := 80 // Default webp quality
	if q, err := strconv.Atoi(qualityStr); err == nil && q > 0 && q <= 100 {
		quality = q
	}

	// Разобрать URL
	u, err := url.Parse(rawImageURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		slog.Warn("Invalid image URL", "url", rawImageURL, "error", err)
		http.Error(w, "Invalid image URL", http.StatusBadRequest)
		return
	}

	// Проверка разрешенных доменов
	if len(AllowedDomains) > 0 {
		allowed := false
		for _, domain := range AllowedDomains {
			if u.Hostname() == domain {
				allowed = true
				break
			}
		}
		if !allowed {
			slog.Warn("Domain not allowed", "domain", u.Hostname())
			http.Error(w, "Domain not allowed", http.StatusForbidden)
			return
		}
	}

	slog.Info("Processing image", "url", rawImageURL, "width", width, "height", height, "radius", radius)

	parts := strings.Split(rawImageURL, "?")
	imageURL := rawImageURL
	updateData := ""
	// Проверка, что в URL есть знак вопроса
	if len(parts) > 1 {
		imageURL = parts[0]
		updateData = parts[1]
	}

	// Получить путь к файлу и очистить его для безопасности
	cleanPath := filepath.Clean(u.Path)
	dir := filepath.Join(StoragePath, filepath.Dir(cleanPath))
	fileName := filepath.Base(cleanPath)
	metaPath := filepath.Join(dir, ".meta")

	filenameWithoutExtension := strings.TrimSuffix(fileName, path.Ext(fileName))
	if width > 0 && height > 0 {
		filenameWithoutExtension = fmt.Sprintf("%s_resized-%dx%d", filenameWithoutExtension, width, height)
		if cropX != "center" || cropY != "center" {
			filenameWithoutExtension = fmt.Sprintf("%s_crop-%s-%s", filenameWithoutExtension, cropX, cropY)
		}
	}
	if radius > 0 {
		filenameWithoutExtension = fmt.Sprintf("%s_radius-%d", filenameWithoutExtension, radius)
	}
	if quality != 80 {
		filenameWithoutExtension = fmt.Sprintf("%s_q-%d", filenameWithoutExtension, quality)
	}

	var ext string
	if format == "webp" {
		ext = "webp"
	} else {
		ext = "png"
	}

	newFilePath := filepath.Join(dir, fmt.Sprintf("%s.%s", filenameWithoutExtension, ext))

	var downloadTime, processTime time.Duration

	sendResponse := func() {
		w.Header().Set("X-Download-Time", downloadTime.String())
		w.Header().Set("X-Processing-Time", processTime.String())
		if hash := getFileHash(newFilePath); hash != "" {
			w.Header().Set("X-Image-Hash", hash)
		}
		if format == "webp" {
			w.Header().Set("Content-Type", "image/webp")
		} else {
			w.Header().Set("Content-Type", "image/png")
		}
		http.ServeFile(w, r, newFilePath)
	}

	// Проверить, существует ли файл в папке
	if service.CheckField(newFilePath) {
		if updateData == "" {
			sendResponse()
			return
		}

		if ReadMeta(metaPath) == updateData {
			sendResponse()
			return
		}

		dropOldData(dir)
	}

	startDownload := time.Now()
	// Скачать изображение
	img, err := service.DownloadImage(fileName, imageURL)
	downloadTime = time.Since(startDownload)
	if err != nil {
		slog.Error("Failed to download image", "url", imageURL, "error", err)
		http.Error(w, "Failed to download image", http.StatusInternalServerError)
		return
	}

	startProcess := time.Now()
	if width > 0 && height > 0 {
		img = imagep.ResizedImage(img, width, height, cropX, cropY)
	}
	if radius > 0 {
		img = imagep.RoundImage(img, radius)
	}
	processTime = time.Since(startProcess)

	// Сохранить изображение
	outputFile, err := service.Save(dir, newFilePath)
	if err != nil {
		slog.Error("Failed to create output file", "path", newFilePath, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if format == "webp" {
		options := webp.Options{Quality: quality}
		err = webp.Encode(outputFile, img, options)
		outputFile.Close()
		if err != nil {
			slog.Error("Failed to encode webp", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
	} else {
		// Для PNG используем CompressionLevel исходя из качества:
		// quality 100 -> BestSpeed (низкое сжатие, быстро),
		// quality <= 80 -> BestCompression (макс сжатие файлов)
		// Напрямую управлять потерями в PNG нельзя, но можно управлять скоростью.
		pngEncoder := png.Encoder{CompressionLevel: png.DefaultCompression}
		if quality >= 90 {
			pngEncoder.CompressionLevel = png.BestSpeed
		} else if quality <= 70 {
			pngEncoder.CompressionLevel = png.BestCompression
		}

		err = pngEncoder.Encode(outputFile, img)
		outputFile.Close()
		if err != nil {
			slog.Error("Failed to encode png", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
	}

	if updateData != "" {
		service.CreateMeta(dir, updateData)
	}

	sendResponse()
}

func ReadMeta(metaPath string) string {
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return ""
	}

	content := string(data)
	keyValuePairs := strings.Split(content, ":")
	if len(keyValuePairs) != 2 {
		return ""
	}

	key := strings.TrimSpace(keyValuePairs[0])
	value := strings.TrimSpace(keyValuePairs[1])

	if key == "lastDate" {
		return value
	}
	return ""
}

func dropOldData(folderPath string) {
	files, err := os.ReadDir(folderPath)
	if err != nil {
		slog.Error("Error reading directory for cleanup", "path", folderPath, "error", err)
		return
	}

	for _, file := range files {
		filePath := filepath.Join(folderPath, file.Name())
		err := os.Remove(filePath)
		if err != nil {
			slog.Warn("Error removing file during cleanup", "path", filePath, "error", err)
		}
	}
}
