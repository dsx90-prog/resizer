package handlers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"resizer/internal/service"
	imagep "resizer/pkg/image"
	videop "resizer/pkg/video"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gen2brain/webp"
)

var AllowedDomains []string
var StoragePath string = "artefacts"       // Default value
var VideoProcessingMode string = "chunked" // Default value
var SignatureEnabled bool = false
var AllowSignatureGen bool = true
var SecurityKey string = ""
var AllowCustomDimensions bool = true
var Presets map[string]PresetConfig

type PresetConfig struct {
	Width   int
	Height  int
	Radius  int
	Quality int
}

func getFileHash(filePath string) string {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func verifySignature(params url.Values, key string) bool {
	receivedSig := params.Get("s")
	if receivedSig == "" {
		return false
	}

	expectedSig := calculateSignature(params, key)
	return hmac.Equal([]byte(receivedSig), []byte(expectedSig))
}

func calculateSignature(params url.Values, key string) string {
	// Create a sorted list of keys to ensure consistent signature
	keys := make([]string, 0, len(params))
	for k := range params {
		if k != "s" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteString("&")
		}
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(params.Get(k))
	}

	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(b.String()))
	return hex.EncodeToString(mac.Sum(nil))
}

func ResizeHandler(w http.ResponseWriter, r *http.Request) {
	// Получить параметры из URL
	params := r.URL.Query()

	// 0. Verify signature if enabled
	if SignatureEnabled {
		if !verifySignature(params, SecurityKey) {
			slog.Warn("Invalid or missing URL signature")
			http.Error(w, "Invalid or missing URL signature ('s' parameter)", http.StatusForbidden)
			return
		}
	}

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

	startSec, _ := strconv.ParseFloat(params.Get("start"), 64)
	endSec, _ := strconv.ParseFloat(params.Get("end"), 64)

	presetName := params.Get("preset")
	if presetName != "" {
		if p, ok := Presets[presetName]; ok {
			slog.Info("Applying preset", "name", presetName)
			width = p.Width
			height = p.Height
			if p.Radius > 0 {
				radius = p.Radius
			}
			if p.Quality > 0 {
				quality = p.Quality
			}
		} else {
			slog.Warn("Preset not found", "name", presetName)
		}
	}

	// If custom dimensions are disabled, check if we are using a preset or just downloading original
	if !AllowCustomDimensions && presetName == "" && (params.Get("width") != "" || params.Get("height") != "" || params.Get("radius") != "") {
		slog.Warn("Custom dimensions are disabled and no preset provided")
		http.Error(w, "Custom dimensions are disabled. Please use a valid 'preset'.", http.StatusForbidden)
		return
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
	if startSec > 0 {
		filenameWithoutExtension = fmt.Sprintf("%s_start-%.2f", filenameWithoutExtension, startSec)
	}
	if endSec > 0 {
		filenameWithoutExtension = fmt.Sprintf("%s_end-%.2f", filenameWithoutExtension, endSec)
	}

	// Сначала мы не знаем точное расширение (mp4/png/webp), поэтому пока просто базовое имя пути к кешу
	cacheBasePath := filepath.Join(dir, filenameWithoutExtension)

	// Мы пока не знаем формат (video или image), поэтому отправим ответ когда процесс завершится
	// Но пока проверим, не лежат ли в кеше уже готовые медиафайлы с нужными параметрами

	// Возможные форматы закэшированного результата
	possibleCacheFiles := []string{
		cacheBasePath + ".mp4",
		cacheBasePath + ".png",
		cacheBasePath + ".webp",
	}

	for _, cachedFile := range possibleCacheFiles {
		if service.CheckField(cachedFile) {
			if updateData == "" || ReadMeta(metaPath) == updateData {
				w.Header().Set("X-Cache", "HIT")

				if strings.HasSuffix(cachedFile, ".webp") {
					w.Header().Set("Content-Type", "image/webp")
				} else if strings.HasSuffix(cachedFile, ".png") {
					w.Header().Set("Content-Type", "image/png")
				} else if strings.HasSuffix(cachedFile, ".mp4") {
					w.Header().Set("Content-Type", "video/mp4")
				}

				http.ServeFile(w, r, cachedFile)
				return
			}
			dropOldData(dir)
			break
		}
	}

	startDownload := time.Now()

	// 1. Start downloading stream to identify content
	resp, err := service.DownloadStream(imageURL)
	if err != nil {
		slog.Error("Failed to start download stream", "url", imageURL, "error", err)
		http.Error(w, "Failed to download file", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")
	isVideo := strings.HasPrefix(contentType, "video/")

	// Read first 512KB for hashing (Smart Content ID)
	previewBuf := make([]byte, 512*1024)
	nBytes, err := io.ReadAtLeast(resp.Body, previewBuf, 1)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		slog.Error("Failed to read header for hashing", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	previewBuf = previewBuf[:nBytes]

	// Early hash based on content bits
	earlyHasher := sha256.New()
	earlyHasher.Write(previewBuf)
	contentID := hex.EncodeToString(earlyHasher.Sum(nil))

	// Path based on Content ID deduplication
	filenameWithContentID := fmt.Sprintf("%s_w-%d_h-%d", contentID, width, height)
	if radius > 0 {
		filenameWithContentID = fmt.Sprintf("%s_r-%d", filenameWithContentID, radius)
	}
	if quality > 0 {
		filenameWithContentID = fmt.Sprintf("%s_q-%d", filenameWithContentID, quality)
	}
	if startSec > 0 {
		filenameWithContentID = fmt.Sprintf("%s_start-%.2f", filenameWithContentID, startSec)
	}
	if endSec > 0 {
		filenameWithContentID = fmt.Sprintf("%s_end-%.2f", filenameWithContentID, endSec)
	}
	contentCacheBasePath := filepath.Join(dir, filenameWithContentID)

	// Check if we have THIS EXACT CONTENT already processed
	for _, ext := range []string{".mp4", ".webp", ".png"} {
		actualCachedFile := contentCacheBasePath + ext
		if service.CheckField(actualCachedFile) {
			slog.Info("Cache HIT by Content ID signature", "id", contentID)
			w.Header().Set("X-Cache", "HIT-ID")
			if ext == ".mp4" {
				w.Header().Set("Content-Type", "video/mp4")
			} else if ext == ".webp" {
				w.Header().Set("Content-Type", "image/webp")
			} else {
				w.Header().Set("Content-Type", "image/png")
			}
			http.ServeFile(w, r, actualCachedFile)
			return
		}
	}

	// Combine what we read and what's left in the pipe
	fullStream := io.MultiReader(bytes.NewReader(previewBuf), resp.Body)
	downloadTime := time.Since(startDownload)

	startProcess := time.Now()
	var finalExt string
	var finalFilePath string

	if isVideo {
		finalExt = "mp4"
		finalFilePath = contentCacheBasePath + "." + finalExt
		service.CreateDirectory(dir)

		opts := videop.ProcessOptions{
			Width:   width,
			Height:  height,
			Quality: quality,
			Start:   startSec,
			End:     endSec,
		}

		if VideoProcessingMode == "stream" {
			slog.Info("Starting STREAMING video processing", "url", imageURL)
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("X-Processing-Mode", "stream")

			// Write simultaneously to response and cache file
			cacheFile, err := os.Create(finalFilePath)
			if err != nil {
				slog.Error("Failed to create cache file", "error", err)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}
			defer cacheFile.Close()

			mw := io.MultiWriter(w, cacheFile)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()

			if err := videop.StreamVideo(ctx, fullStream, mw, opts); err != nil {
				slog.Error("Streaming video failed", "error", err)
				return
			}
			return // Stream finished and sent
		} else {
			slog.Info("Starting CHUNKED video processing", "url", imageURL)
			// Chunked mode needs file on disk
			tempFile, err := os.CreateTemp("", "resizer_stream_*.mp4")
			if err != nil {
				slog.Error("Failed to create temp file", "error", err)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}
			tempPath := tempFile.Name()
			defer os.Remove(tempPath)

			if _, err := io.Copy(tempFile, fullStream); err != nil {
				tempFile.Close()
				slog.Error("Failed to save stream to temp file", "error", err)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}
			tempFile.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()

			if err := videop.ProcessVideo(ctx, tempPath, finalFilePath, opts); err != nil {
				slog.Error("Chunked video processing failed", "error", err)
				http.Error(w, "Video processing failed", http.StatusInternalServerError)
				return
			}
		}
	} else {
		// Image processing using fullStream
		img, err := imagep.DecodeImage(fileName, fullStream)
		if err != nil {
			slog.Error("Failed to decode image", "error", err)
			http.Error(w, "Failed to decode image", http.StatusBadRequest)
			return
		}

		if width > 0 && height > 0 {
			img = imagep.ResizedImage(img, width, height, cropX, cropY)
		}
		if radius > 0 {
			img = imagep.RoundImage(img, radius)
		}

		if format == "webp" {
			finalExt = "webp"
		} else {
			finalExt = "png"
		}
		finalFilePath = contentCacheBasePath + "." + finalExt

		outputFile, err := service.Save(dir, finalFilePath)
		if err != nil {
			slog.Error("Failed to create image output file", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		if format == "webp" {
			options := webp.Options{Quality: quality}
			err = webp.Encode(outputFile, img, options)
		} else {
			pngEncoder := png.Encoder{CompressionLevel: png.DefaultCompression}
			if quality >= 90 {
				pngEncoder.CompressionLevel = png.BestSpeed
			} else if quality <= 70 {
				pngEncoder.CompressionLevel = png.BestCompression
			}
			err = pngEncoder.Encode(outputFile, img)
		}
		outputFile.Close()

		if err != nil {
			slog.Error("Failed to encode image", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
	}

	processTime := time.Since(startProcess)

	if updateData != "" {
		service.CreateMeta(dir, updateData)
	}

	w.Header().Set("X-Download-Time", downloadTime.String())
	w.Header().Set("X-Processing-Time", processTime.String())
	if hash := getFileHash(finalFilePath); hash != "" {
		w.Header().Set("X-Image-Hash", hash)
	}

	if finalExt == "webp" {
		w.Header().Set("Content-Type", "image/webp")
	} else if finalExt == "png" {
		w.Header().Set("Content-Type", "image/png")
	} else if finalExt == "mp4" {
		w.Header().Set("Content-Type", "video/mp4")
	}

	http.ServeFile(w, r, finalFilePath)
}

func HashCheckHandler(w http.ResponseWriter, r *http.Request) {
	hash := r.URL.Query().Get("hash")
	if hash == "" {
		http.Error(w, "Parameter 'hash' is required", http.StatusBadRequest)
		return
	}

	type match struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	var matches []match

	// Recursive search in StoragePath
	err := filepath.Walk(StoragePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // ignore errors, continue walking
		}
		if !info.IsDir() && strings.HasPrefix(info.Name(), hash) {
			relPath, _ := filepath.Rel(StoragePath, path)
			matches = append(matches, match{
				Path: relPath,
				Size: info.Size(),
			})
		}
		return nil
	})

	if err != nil {
		slog.Error("Failed to walk storage path", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	response := struct {
		Exists  bool    `json:"exists"`
		Hash    string  `json:"hash"`
		Matches []match `json:"matches"`
	}{
		Exists:  len(matches) > 0,
		Hash:    hash,
		Matches: matches,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func URLInfoHandler(w http.ResponseWriter, r *http.Request) {
	rawURL := r.URL.Query().Get("url")
	if rawURL == "" {
		http.Error(w, "Parameter 'url' is required", http.StatusBadRequest)
		return
	}

	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		http.Error(w, "Invalid URL", http.StatusBadRequest)
		return
	}

	// Logic to find the directory (same as ResizeHandler)
	cleanPath := filepath.Clean(u.Path)
	dir := filepath.Join(StoragePath, filepath.Dir(cleanPath))

	files, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"exists": false,
				"url":    rawURL,
			})
			return
		}
		slog.Error("Failed to read dir", "dir", dir, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	var foundHash string
	var fileList []string
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		name := f.Name()
		// Avoid meta files or internal stuff
		if strings.HasPrefix(name, ".") || name == "concat_list.txt" {
			continue
		}

		// Extract hash (before first underscore)
		parts := strings.Split(name, "_")
		if len(parts) > 1 && len(parts[0]) == 64 { // SHA-256 hex length
			foundHash = parts[0]
		}
		fileList = append(fileList, name)
	}

	response := struct {
		Exists bool     `json:"exists"`
		URL    string   `json:"url"`
		Hash   string   `json:"hash,omitempty"`
		Files  []string `json:"files,omitempty"`
	}{
		Exists: len(fileList) > 0,
		URL:    rawURL,
		Hash:   foundHash,
		Files:  fileList,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func SignatureGenHandler(w http.ResponseWriter, r *http.Request) {
	if !AllowSignatureGen {
		slog.Warn("Signature generation is disabled by config")
		http.Error(w, "Signature generation is disabled", http.StatusForbidden)
		return
	}
	if SecurityKey == "" {
		http.Error(w, "Security key is not configured", http.StatusInternalServerError)
		return
	}

	params := r.URL.Query()
	if params.Get("url") == "" {
		http.Error(w, "Parameter 'url' is required to generate signature", http.StatusBadRequest)
		return
	}

	sig := calculateSignature(params, SecurityKey)

	// Build the signed path
	params.Set("s", sig)
	signedQuery := params.Encode()

	response := struct {
		Signature string `json:"signature"`
		SignedURL string `json:"signed_url"`
	}{
		Signature: sig,
		SignedURL: "/?" + signedQuery,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
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
