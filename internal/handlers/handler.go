package handlers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
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
	"resizer/pkg/storage"
	videop "resizer/pkg/video"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gen2brain/webp"
	"github.com/koyachi/go-nude"
)

var AllowedDomains []string
var StoragePath string = "artefacts"       // Default value
var VideoProcessingMode string = "chunked" // Default value
var SignatureEnabled bool = false
var AllowSignatureGen bool = true
var SecurityKey string = ""
var AllowCustomDimensions bool = true
var Presets map[string]PresetConfig
var GlobalStore storage.StorageProvider

var DraftEnabled bool = false
var DraftTTL time.Duration = time.Hour
var DraftPath string = "temp_drafts"

var NudeCheckEnabled bool = false
var FailOnNude bool = true

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

func saveToStore(ctx context.Context, path string, data io.Reader, expiresAt time.Time) error {
	if DraftEnabled || !expiresAt.IsZero() {
		// Save locally to temp_drafts
		fullPath := filepath.Join(DraftPath, path)
		os.MkdirAll(filepath.Dir(fullPath), 0755)
		f, err := os.Create(fullPath)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(f, data)
		if err != nil {
			return err
		}

		// Save TTL meta if specific expiration provided
		if !expiresAt.IsZero() {
			ttlPath := fullPath + ".ttl"
			os.WriteFile(ttlPath, []byte(strconv.FormatInt(expiresAt.Unix(), 10)), 0644)
		}

		slog.Info("Saved to DRAFT storage", "path", path, "expiresAt", expiresAt)
		return nil
	}
	return GlobalStore.Save(ctx, path, data)
}

func ResizeHandler(w http.ResponseWriter, r *http.Request) {
	// Получить параметры из URL
	params := r.URL.Query()

	var expiration time.Time
	if dttl := params.Get("draft_ttl"); dttl != "" {
		// Try duration first (1h, 30m)
		if dur, err := time.ParseDuration(dttl); err == nil {
			expiration = time.Now().Add(dur)
		} else {
			// Try date YYYY-MM-DD
			if t, err := time.Parse("2006-01-02", dttl); err == nil {
				// Set to end of day (23:59:59)
				expiration = time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, t.Location())
			}
		}
	}

	nudeCheckReq := r.URL.Query().Get("nude_check") == "1" || r.URL.Query().Get("nude_check") == "true"
	doNudeCheck := NudeCheckEnabled || nudeCheckReq

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
	// cacheBasePath := filepath.Join(dir, filenameWithoutExtension)

	// Мы пока не знаем формат (video или image), поэтому отправим ответ когда процесс завершится
	// Но пока проверим, не лежат ли в кеше уже готовые медиафайлы с нужными параметрами

	// Возможные форматы закэшированного результата
	possibleCacheFiles := []string{
		filenameWithoutExtension + ".mp4",
		filenameWithoutExtension + ".png",
		filenameWithoutExtension + ".webp",
	}

	for _, cachedKey := range possibleCacheFiles {
		fullKey := filepath.Join(filepath.Dir(u.Path), cachedKey)
		exists, _ := GlobalStore.Exists(r.Context(), fullKey)
		if exists {
			// For info endpoint or similar we might need the original logic,
			// but here we serve the file.
			if updateData == "" || ReadMeta(metaPath) == updateData {
				w.Header().Set("X-Cache", "HIT")

				if strings.HasSuffix(cachedKey, ".webp") {
					w.Header().Set("Content-Type", "image/webp")
				} else if strings.HasSuffix(cachedKey, ".png") {
					w.Header().Set("Content-Type", "image/png")
				} else if strings.HasSuffix(cachedKey, ".mp4") {
					w.Header().Set("Content-Type", "video/mp4")
				}

				if localPath, isLocal := GlobalStore.LocalPath(fullKey); isLocal {
					http.ServeFile(w, r, localPath)
				} else {
					// Check draft first if S3
					if DraftEnabled {
						draftLocal := filepath.Join(DraftPath, fullKey)
						if _, err := os.Stat(draftLocal); err == nil {
							w.Header().Set("X-Cache", "HIT-DRAFT")
							http.ServeFile(w, r, draftLocal)
							return
						}
					}

					reader, err := GlobalStore.GetReader(r.Context(), fullKey)
					if err == nil {
						defer reader.Close()
						io.Copy(w, reader)
					}
				}
				return
			}
			// If meta doesn't match, we might need to delete old data in local mode
			// but for S3 it's more complex. For now, let's keep it simple.
			break
		}
	}

	// startDownload := time.Now()

	// 1. Start downloading stream to identify content
	resp, err := service.DownloadStream(imageURL, r.Header)
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
	// contentCacheBasePath := filepath.Join(dir, filenameWithContentID)

	// Check if we have THIS EXACT CONTENT already processed
	for _, ext := range []string{".mp4", ".webp", ".png"} {
		actualKey := filepath.Join(filepath.Dir(u.Path), filenameWithContentID+ext)
		exists, _ := GlobalStore.Exists(r.Context(), actualKey)

		// Check draft too
		draftLocal := filepath.Join(DraftPath, actualKey)
		draftExists := false
		if DraftEnabled {
			if _, err := os.Stat(draftLocal); err == nil {
				draftExists = true
			}
		}

		if exists || draftExists {
			slog.Info("Cache HIT by Content ID signature", "id", contentID)
			w.Header().Set("X-Cache", "HIT-ID")
			if draftExists {
				w.Header().Set("X-Cache", "HIT-ID-DRAFT")
			}

			if ext == ".mp4" {
				w.Header().Set("Content-Type", "video/mp4")
			} else if ext == ".webp" {
				w.Header().Set("Content-Type", "image/webp")
			} else {
				w.Header().Set("Content-Type", "image/png")
			}

			if draftExists {
				http.ServeFile(w, r, draftLocal)
				return
			}

			if localPath, isLocal := GlobalStore.LocalPath(actualKey); isLocal {
				http.ServeFile(w, r, localPath)
			} else {
				reader, err := GlobalStore.GetReader(r.Context(), actualKey)
				if err == nil {
					defer reader.Close()
					io.Copy(w, reader)
				}
			}
			return
		}
	}

	// Combine what we read and what's left in the pipe
	fullStream := io.MultiReader(bytes.NewReader(previewBuf), resp.Body)
	// downloadTime := time.Since(startDownload)

	ext := ".png"
	if isVideo {
		ext = ".mp4"
	}
	actualKey := filepath.Join(filepath.Dir(u.Path), filenameWithContentID+ext)

	if isVideo {
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

			// If S3, we need a temp file to cache the result while streaming
			// Or we can try to use a pipe, but FFmpeg usually needs a seekable file for some outputs.
			// However, StreamVideo is designed to stream.

			var cacheFile *os.File
			var tempPath string
			var err error

			if _, isLocal := GlobalStore.LocalPath(actualKey); isLocal {
				fullPath, _ := GlobalStore.LocalPath(actualKey)
				os.MkdirAll(filepath.Dir(fullPath), 0755)
				cacheFile, err = os.Create(fullPath)
			} else {
				cacheFile, err = os.CreateTemp("", "resizer_vcache_*.mp4")
				tempPath = cacheFile.Name()
			}

			if err != nil {
				slog.Error("Failed to create cache file", "error", err)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}
			defer cacheFile.Close()
			if tempPath != "" {
				defer os.Remove(tempPath)
			}

			mw := io.MultiWriter(w, cacheFile)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()

			if err := videop.StreamVideo(ctx, fullStream, mw, opts); err != nil {
				slog.Error("Streaming video failed", "error", err)
				return
			}

			if tempPath != "" {
				// Upload to Store / Draft
				cacheFile.Seek(0, 0)
				saveToStore(context.Background(), actualKey, cacheFile, expiration)
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

			outputTemp, err := os.CreateTemp("", "resizer_vout_*.mp4")
			if err != nil {
				slog.Error("Failed to create output temp file", "error", err)
				return
			}
			outputTempPath := outputTemp.Name()
			outputTemp.Close()
			defer os.Remove(outputTempPath)

			if err := videop.ProcessVideo(ctx, tempPath, outputTempPath, opts); err != nil {
				slog.Error("Chunked video processing failed", "error", err)
				http.Error(w, "Video processing failed", http.StatusInternalServerError)
				return
			}

			// Save from temp to Store / Draft
			f, err := os.Open(outputTempPath)
			if err == nil {
				saveToStore(ctx, actualKey, f, expiration)
				f.Close()
			}

			// Serve to client
			f, err = os.Open(outputTempPath)
			if err == nil {
				defer f.Close()
				w.Header().Set("Content-Type", "video/mp4")
				io.Copy(w, f)
			}
			return
		}
	} else {
		// Image processing
		var img image.Image
		var err error

		if doNudeCheck {
			// go-nude needs a file path
			tempFile, err := os.CreateTemp("", "resizer_nude_*.tmp")
			if err != nil {
				slog.Error("Failed to create temp file for nude check", "error", err)
			} else {
				tempPath := tempFile.Name()
				defer os.Remove(tempPath)

				if _, err := io.Copy(tempFile, fullStream); err != nil {
					slog.Error("Failed to save stream to temp for nude check", "error", err)
				}
				tempFile.Close()

				// Perform check
				isNude, err := nude.IsNude(tempPath)
				if err != nil {
					slog.Warn("Nudity check failed", "error", err)
				} else if isNude {
					w.Header().Set("X-Nude", "true")
					if FailOnNude {
						slog.Info("Nudity detected, blocking request")
						http.Error(w, "Forbidden: Nudity detected", http.StatusForbidden)
						return
					}
					slog.Info("Nudity detected, allowing but marking")
				}

				// Now decode from the temp file since stream is consumed
				f, _ := os.Open(tempPath)
				defer f.Close()
				img, err = imagep.DecodeImage(fileName, f)
			}
		} else {
			// Normal decoding from stream
			img, err = imagep.DecodeImage(fileName, fullStream)
		}

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

		imgExt := format
		if imgExt == "" {
			imgExt = "png"
		}
		actualKey := filepath.Join(filepath.Dir(u.Path), filenameWithContentID+"."+imgExt)

		// Encode to buffer or temp file
		buf := new(bytes.Buffer)
		if format == "webp" {
			options := webp.Options{Quality: quality}
			err = webp.Encode(buf, img, options)
		} else {
			pngEncoder := png.Encoder{CompressionLevel: png.DefaultCompression}
			if quality >= 90 {
				pngEncoder.CompressionLevel = png.BestSpeed
			} else if quality <= 70 {
				pngEncoder.CompressionLevel = png.BestCompression
			}
			err = pngEncoder.Encode(buf, img)
		}

		if err != nil {
			slog.Error("Failed to encode image", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		// Save to store / Draft
		if err := saveToStore(r.Context(), actualKey, buf, expiration); err != nil {
			slog.Error("Failed to save image to store", "error", err)
		}

		// Return to client (since it's already in buffer, it's easy)
		if format == "webp" {
			w.Header().Set("Content-Type", "image/webp")
		} else {
			w.Header().Set("Content-Type", "image/png")
		}
		w.Write(buf.Bytes())
		return
	}
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

	// Store-based search
	prefix := hash // In S3 we use prefix
	files, err := GlobalStore.List(r.Context(), prefix)
	if err != nil {
		slog.Error("Failed to list storage", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	for _, name := range files {
		matches = append(matches, match{
			Path: name,
			Size: 0, // We don't have size from List easily without extra calls
		})
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
	prefix := filepath.Dir(u.Path)
	files, err := GlobalStore.List(r.Context(), prefix)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"exists": false,
			"url":    rawURL,
		})
		return
	}

	var foundHash string
	var fileList []string
	for _, name := range files {
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

func ConfirmHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	targetPath := r.URL.Query().Get("path")
	targetHash := r.URL.Query().Get("hash")

	if targetPath == "" && targetHash == "" {
		http.Error(w, "Parameter 'path' or 'hash' is required", http.StatusBadRequest)
		return
	}

	var confirmedPaths []string
	ctx := r.Context()

	if targetPath != "" {
		// Confirm specific path
		draftFile := filepath.Join(DraftPath, targetPath)
		if err := confirmFile(ctx, targetPath, draftFile); err != nil {
			if os.IsNotExist(err) {
				http.Error(w, "Draft not found or expired", http.StatusNotFound)
				return
			}
			slog.Error("Failed to confirm draft by path", "path", targetPath, "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		confirmedPaths = append(confirmedPaths, targetPath)
	} else if targetHash != "" {
		// Confirm everything matching hash (recursive search)
		err := filepath.Walk(DraftPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || strings.HasSuffix(path, ".ttl") {
				return nil
			}

			if strings.HasPrefix(info.Name(), targetHash) {
				// Convert absolute local path back to relative storage path
				relPath, err := filepath.Rel(DraftPath, path)
				if err != nil {
					return nil
				}
				if err := confirmFile(ctx, relPath, path); err != nil {
					slog.Warn("Failed to confirm draft during hash sweep", "path", relPath, "error", err)
					return nil
				}
				confirmedPaths = append(confirmedPaths, relPath)
			}
			return nil
		})

		if err != nil {
			slog.Error("Failed to walk draft directory for hash confirmation", "hash", targetHash, "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		if len(confirmedPaths) == 0 {
			http.Error(w, "No drafts found for this hash", http.StatusNotFound)
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "confirmed",
		"hash":      targetHash,
		"confirmed": confirmedPaths,
	})
}

func confirmFile(ctx context.Context, targetPath, draftFile string) error {
	f, err := os.Open(draftFile)
	if err != nil {
		return err
	}
	defer f.Close()

	// Upload to main GlobalStore
	if err := GlobalStore.Save(ctx, targetPath, f); err != nil {
		return err
	}

	// Remove from local drafts
	os.Remove(draftFile)
	os.Remove(draftFile + ".ttl")
	return nil
}

func CleanupDrafts() {
	if !DraftEnabled {
		return
	}

	files, err := os.ReadDir(DraftPath)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		slog.Error("Failed to read draft directory for cleanup", "path", DraftPath, "error", err)
		return
	}

	now := time.Now()
	for _, f := range files {
		if f.IsDir() || strings.HasSuffix(f.Name(), ".ttl") {
			continue
		}

		fullPath := filepath.Join(DraftPath, f.Name())
		ttlPath := fullPath + ".ttl"

		var expiration time.Time
		if ttlData, err := os.ReadFile(ttlPath); err == nil {
			if ts, err := strconv.ParseInt(string(ttlData), 10, 64); err == nil {
				expiration = time.Unix(ts, 0)
			}
		}

		if expiration.IsZero() {
			info, err := f.Info()
			if err != nil {
				continue
			}
			expiration = info.ModTime().Add(DraftTTL)
		}

		if now.After(expiration) {
			slog.Info("Deleting expired draft", "path", fullPath, "expiration", expiration)
			os.Remove(fullPath)
			os.Remove(ttlPath)
		}
	}
}
