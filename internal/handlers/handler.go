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

	"github.com/corona10/goimagehash"
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
var GlobalStore storage.StorageProvider

var DraftEnabled bool = false
var DraftTTL time.Duration = time.Hour
var DraftPath string = "temp_drafts"

var NudeCheckEnabled bool = false
var FailOnNude bool = true
var NudeBlurEnabled bool = false
var NudeBlurStrength int = 50
var NudeThreshold float64 = 0.5 // ML confidence threshold (0.0 - 1.0)
var BlockedCategories []string = []string{"Porn", "Hentai"}
var ModelPath string = "models/nsfw_mobilenet.onnx"

var ObjDetectionEnabled bool = false
var ObjDetectionThreshold float64 = 0.1

type PresetConfig struct {
	Width   int
	Height  int
	Radius  int
	Quality int
}

type MediaParams struct {
	Width      int
	Height     int
	Radius     int
	Quality    int
	Format     string
	CropX      string
	CropY      string
	Start      float64
	End        float64
	NudeCheck  bool
	NudeBlur   bool
	Blur       int
	Info       bool
	Expiration time.Time
}

func getFileHash(filePath string) string {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

type ImageHashes struct {
	Average         string                   `json:"a_hash"`
	Perceptual      string                   `json:"p_hash"`
	Difference      string                   `json:"d_hash"`
	CreatedAt       time.Time                `json:"upload_date"`
	IsNude          bool                     `json:"is_nude_internal,omitempty"`
	Classification  map[string]float64       `json:"nude_classification_internal,omitempty"`
	DetectedObjects []service.Classification `json:"detected_objects_internal,omitempty"`
}

func (h ImageHashes) Clean() map[string]interface{} {
	return map[string]interface{}{
		"a_hash":      h.Average,
		"p_hash":      h.Perceptual,
		"d_hash":      h.Difference,
		"upload_date": h.CreatedAt,
	}
}

func calculateImageHashes(img image.Image, isNude bool, classification map[string]float64, objects []service.Classification) ImageHashes {
	aHash, _ := goimagehash.AverageHash(img)
	pHash, _ := goimagehash.PerceptionHash(img)
	dHash, _ := goimagehash.DifferenceHash(img)

	return ImageHashes{
		Average:         aHash.ToString(),
		Perceptual:      pHash.ToString(),
		Difference:      dHash.ToString(),
		CreatedAt:       time.Now(),
		IsNude:          isNude,
		Classification:  classification,
		DetectedObjects: objects,
	}
}

func populateInfoFromHashes(info map[string]interface{}, hashes ImageHashes) {
	info["hashes"] = hashes.Clean()
	info["upload_date"] = hashes.CreatedAt
	info["is_nude"] = hashes.IsNude

	var descriptionParts []string

	// Find top NSFW category
	var topNSFW string
	var maxProb float64
	for cat, prob := range hashes.Classification {
		if prob > maxProb {
			maxProb = prob
			topNSFW = cat
		}
	}
	if topNSFW != "" {
		descriptionParts = append(descriptionParts, topNSFW)
	}

	// Add object labels
	for _, obj := range hashes.DetectedObjects {
		descriptionParts = append(descriptionParts, obj.Label)
	}

	if len(descriptionParts) > 0 {
		info["description"] = strings.Join(descriptionParts, ", ")
	}
}

// isNudeWithML использует предобученную модель ONNX для классификации изображения.
func isNudeWithML(img image.Image, threshold float64) (bool, map[string]float64, error) {
	if img == nil {
		return false, nil, fmt.Errorf("nil image provided")
	}

	_, probs, err := service.IsNudeML(img, threshold)
	if err != nil {
		slog.Error("ML Nudity check failed", "error", err)
		return false, nil, err
	}

	// Check if any of the blocked categories exceed the threshold
	isBlocked := false
	for _, cat := range BlockedCategories {
		if prob, ok := probs[cat]; ok && prob > threshold {
			isBlocked = true
			break
		}
	}

	slog.Info("ML Nudity check result", "is_nude", isBlocked, "probs", probs)
	return isBlocked, probs, nil
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
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
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
	nudeBlurReq := r.URL.Query().Get("nude_blur") == "1" || r.URL.Query().Get("nude_blur") == "true"
	infoReq := r.URL.Query().Get("info") == "1" || r.URL.Query().Get("info") == "true"
	blur, _ := strconv.Atoi(params.Get("blur"))
	doNudeBlur := NudeBlurEnabled || nudeBlurReq
	doNudeCheck := NudeCheckEnabled || nudeCheckReq || doNudeBlur

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
	if width > 0 || height > 0 {
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
	if blur > 0 {
		filenameWithoutExtension = fmt.Sprintf("%s_blur-%d", filenameWithoutExtension, blur)
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

				if params.Get("info") == "1" || params.Get("info") == "true" {
					w.Header().Set("Content-Type", "application/json")

					draftLocal := filepath.Join(DraftPath, fullKey)
					isDraft := false
					if _, err := os.Stat(draftLocal); err == nil {
						isDraft = true
					}

					info := map[string]interface{}{
						"is_cache_hit": true,
						"path":         fullKey,
						"storage_type": GlobalStore.Type(),
						"is_draft":     isDraft,
					}

					// Try to load hashes if they exist
					hashKey := fullKey + ".hashes"
					var hashesReader io.ReadCloser
					var hErr error

					if isDraft {
						hashesReader, hErr = os.Open(draftLocal + ".hashes")
					} else {
						hashesReader, hErr = GlobalStore.GetReader(r.Context(), hashKey)
					}

					if hErr == nil {
						defer hashesReader.Close()
						var hashes ImageHashes
						if err := json.NewDecoder(hashesReader).Decode(&hashes); err == nil {
							info["hashes"] = hashes
							info["upload_date"] = hashes.CreatedAt
						}
					}

					// Check for draft TTL
					if isDraft {
						ttlPath := draftLocal + ".ttl"
						if ttlData, err := os.ReadFile(ttlPath); err == nil {
							if ts, err := strconv.ParseInt(string(ttlData), 10, 64); err == nil {
								info["draft_expiration"] = time.Unix(ts, 0)
							}
						}
					}

					json.NewEncoder(w).Encode(info)
					return
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
	if blur > 0 {
		filenameWithContentID = fmt.Sprintf("%s_blur-%d", filenameWithContentID, blur)
	}
	// contentCacheBasePath := filepath.Join(dir, filenameWithContentID)

	// Check if we have THIS EXACT CONTENT already processed
	for _, ext := range []string{".mp4", ".webp", ".png"} {
		actualKey := filepath.Join(filepath.Dir(u.Path), filenameWithContentID+ext)
		exists, _ := GlobalStore.Exists(r.Context(), actualKey)

		// Check draft too
		draftLocal := filepath.Join(DraftPath, actualKey)
		draftExists := false
		if _, err := os.Stat(draftLocal); err == nil {
			draftExists = true
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

			if params.Get("info") == "1" || params.Get("info") == "true" {
				w.Header().Set("Content-Type", "application/json")
				info := map[string]interface{}{
					"is_cache_hit": true,
					"content_id":   contentID,
					"path":         actualKey,
					"is_draft":     draftExists,
					"storage_type": GlobalStore.Type(),
				}
				hashKey := actualKey + ".hashes"
				if reader, err := GlobalStore.GetReader(r.Context(), hashKey); err == nil {
					defer reader.Close()
					var hashes ImageHashes
					if err := json.NewDecoder(reader).Decode(&hashes); err == nil {
						populateInfoFromHashes(info, hashes)
					}
				}
				if draftExists {
					ttlPath := draftLocal + ".ttl"
					if ttlData, err := os.ReadFile(ttlPath); err == nil {
						if ts, err := strconv.ParseInt(string(ttlData), 10, 64); err == nil {
							info["draft_expiration"] = time.Unix(ts, 0)
						}
					}
				}
				json.NewEncoder(w).Encode(info)
				return
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

	mParams := MediaParams{
		Width:      width,
		Height:     height,
		Radius:     radius,
		Quality:    quality,
		Format:     format,
		CropX:      cropX,
		CropY:      cropY,
		Start:      startSec,
		End:        endSec,
		NudeCheck:  doNudeCheck,
		NudeBlur:   doNudeBlur,
		Blur:       blur,
		Info:       infoReq,
		Expiration: expiration,
	}

	processMedia(w, r, fullStream, fileName, actualKey, isVideo, mParams)
}

func sendJSONInfo(w http.ResponseWriter, storageKey string, isVideo bool, params MediaParams, img image.Image, isNude bool, classification map[string]float64, objects []service.Classification) {
	info := map[string]interface{}{
		"path":         storageKey,
		"is_draft":     DraftEnabled || !params.Expiration.IsZero(),
		"storage_type": GlobalStore.Type(),
		"upload_date":  time.Now(),
	}

	if !params.Expiration.IsZero() {
		info["draft_expiration"] = params.Expiration
	}

	if !isVideo {
		if img != nil {
			hashes := calculateImageHashes(img, isNude, classification, objects)
			populateInfoFromHashes(info, hashes)
			info["metadata"] = map[string]interface{}{
				"width":  img.Bounds().Dx(),
				"height": img.Bounds().Dy(),
				"format": params.Format,
			}
		} else {
			// Fallback if img is nil but we have data
			info["is_nude"] = isNude
			var descriptionParts []string

			// Top NSFW category
			var topNSFW string
			var maxProb float64
			for cat, prob := range classification {
				if prob > maxProb {
					maxProb = prob
					topNSFW = cat
				}
			}
			if topNSFW != "" {
				descriptionParts = append(descriptionParts, topNSFW)
			}

			// Top objects
			for _, obj := range objects {
				descriptionParts = append(descriptionParts, obj.Label)
			}

			if len(descriptionParts) > 0 {
				info["description"] = strings.Join(descriptionParts, ", ")
			}
		}
	} else {
		info["metadata"] = map[string]interface{}{
			"width":  params.Width,
			"height": params.Height,
			"format": "mp4",
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

func processMedia(w http.ResponseWriter, r *http.Request, stream io.Reader, fileName, storageKey string, isVideo bool, params MediaParams) {
	if params.Info {
		defer func() {
			// If we are just requesting info, we might still want to process and cache?
			// The original request was "return info instead of image".
			// To get hashes and dimensions, we MUST decode/process.
		}()
	}

	if isVideo {
		opts := videop.ProcessOptions{
			Width:   params.Width,
			Height:  params.Height,
			Quality: params.Quality,
			Start:   params.Start,
			End:     params.End,
		}

		if VideoProcessingMode == "stream" {
			slog.Info("Starting STREAMING video processing", "file", fileName)
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("X-Processing-Mode", "stream")

			var cacheFile *os.File
			var tempPath string
			var err error

			if _, isLocal := GlobalStore.LocalPath(storageKey); isLocal {
				fullPath, _ := GlobalStore.LocalPath(storageKey)
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

			if err := videop.StreamVideo(ctx, stream, mw, opts); err != nil {
				slog.Error("Streaming video failed", "error", err)
				return
			}

			if params.Info {
				sendJSONInfo(w, storageKey, true, params, nil, false, nil, nil)
			}

			if tempPath != "" {
				cacheFile.Seek(0, 0)
				saveToStore(context.Background(), storageKey, cacheFile, params.Expiration)
			}
			return
		} else {
			slog.Info("Starting CHUNKED video processing", "file", fileName)
			tempFile, err := os.CreateTemp("", "resizer_stream_*.mp4")
			if err != nil {
				slog.Error("Failed to create temp file", "error", err)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}
			tempPath := tempFile.Name()
			defer os.Remove(tempPath)

			if _, err := io.Copy(tempFile, stream); err != nil {
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

			f, err := os.Open(outputTempPath)
			if err == nil {
				saveToStore(ctx, storageKey, f, params.Expiration)
				f.Close()
			}

			f, err = os.Open(outputTempPath)
			if err == nil {
				defer f.Close()
				if params.Info {
					sendJSONInfo(w, storageKey, true, params, nil, false, nil, nil)
				} else {
					w.Header().Set("Content-Type", "video/mp4")
					io.Copy(w, f)
				}
			}
			return
		}
	} else {
		// Image processing
		var img image.Image
		var err error
		var isNude bool

		img, err = imagep.DecodeImage(fileName, stream)
		if err != nil {
			slog.Error("Failed to decode image", "error", err)
			http.Error(w, "Failed to decode image", http.StatusBadRequest)
			return
		}

		var (
			classification map[string]float64
			objects        []service.Classification
		)
		if params.NudeCheck {
			isNude, classification, err = isNudeWithML(img, NudeThreshold)
			if err != nil {
				slog.Warn("Nudity check failed", "error", err)
			} else if isNude {
				w.Header().Set("X-Nude", "true")
				if params.NudeBlur {
					w.Header().Set("X-Nude", "blurred")
				} else if FailOnNude {
					slog.Info("Nudity detected, blocking request")
					http.Error(w, "Forbidden: Nudity detected", http.StatusForbidden)
					return
				}
			}
		}

		if ObjDetectionEnabled {
			objects, err = service.ClassifyImage(img, ObjDetectionThreshold)
			if err != nil {
				slog.Warn("Object detection failed", "error", err)
			}
		}

		if err != nil {
			slog.Error("Failed to decode image", "error", err)
			http.Error(w, "Failed to decode image", http.StatusBadRequest)
			return
		}

		if params.Width > 0 || params.Height > 0 {
			img = imagep.ResizedImage(img, params.Width, params.Height, params.CropX, params.CropY)
		}
		if params.Radius > 0 {
			img = imagep.RoundImage(img, params.Radius)
		}

		// Apply blur if requested via param OR if nudity detected
		if params.Blur > 0 {
			img = imagep.BlurImage(img, params.Blur)
		} else if w.Header().Get("X-Nude") == "blurred" {
			img = imagep.BlurImage(img, NudeBlurStrength)
		}

		imgExt := params.Format
		if imgExt == "" {
			imgExt = "png"
		}

		buf := new(bytes.Buffer)
		if params.Format == "webp" {
			if params.Quality == 100 {
				err = imagep.EncodeLosslessWebP(buf, img)
			} else {
				options := webp.Options{Quality: params.Quality}
				err = webp.Encode(buf, img, options)
			}
		} else {
			pngEncoder := png.Encoder{CompressionLevel: png.DefaultCompression}
			if params.Quality >= 90 {
				pngEncoder.CompressionLevel = png.BestSpeed
			} else if params.Quality <= 70 {
				pngEncoder.CompressionLevel = png.BestCompression
			}
			err = pngEncoder.Encode(buf, img)
		}

		if err != nil {
			slog.Error("Failed to encode image", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		hashes := calculateImageHashes(img, isNude, classification, objects)
		hashesData, _ := json.Marshal(hashes)

		if err := saveToStore(r.Context(), storageKey, bytes.NewReader(buf.Bytes()), params.Expiration); err != nil {
			slog.Error("Failed to save image to store", "error", err)
		}

		hashKey := storageKey + ".hashes"
		saveToStore(r.Context(), hashKey, bytes.NewReader(hashesData), params.Expiration)

		if params.Format == "webp" {
			w.Header().Set("Content-Type", "image/webp")
		} else {
			w.Header().Set("Content-Type", "image/png")
		}

		if params.Info {
			sendJSONInfo(w, storageKey, false, params, img, isNude, classification, objects)
		} else {
			w.Write(buf.Bytes())
		}
	}
}

func UploadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST method is allowed", http.StatusMethodNotAllowed)
		return
	}

	// 100MB max memory for multipart form
	if err := r.ParseMultipartForm(100 << 20); err != nil {
		slog.Error("Failed to parse multipart form", "error", err)
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "File is required (parameter 'file')", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Parse parameters from form
	width, _ := strconv.Atoi(r.FormValue("width"))
	height, _ := strconv.Atoi(r.FormValue("height"))
	radius, _ := strconv.Atoi(r.FormValue("radius"))

	cropX := r.FormValue("crop_x")
	if cropX == "" {
		cropX = "center"
	}
	cropY := r.FormValue("crop_y")
	if cropY == "" {
		cropY = "center"
	}

	format := strings.ToLower(r.FormValue("format"))
	if format == "" {
		format = "png"
	}

	qualityStr := r.FormValue("q")
	if qualityStr == "" {
		qualityStr = r.FormValue("quality")
	}
	quality := 80
	if q, err := strconv.Atoi(qualityStr); err == nil && q > 0 && q <= 100 {
		quality = q
	}

	startSec, _ := strconv.ParseFloat(r.FormValue("start"), 64)
	endSec, _ := strconv.ParseFloat(r.FormValue("end"), 64)

	infoReq := r.FormValue("info") == "1" || r.FormValue("info") == "true"

	presetName := r.FormValue("preset")
	if presetName != "" {
		if p, ok := Presets[presetName]; ok {
			width = p.Width
			height = p.Height
			if p.Radius > 0 {
				radius = p.Radius
			}
			if p.Quality > 0 {
				quality = p.Quality
			}
		}
	}

	nudeCheckReq := r.FormValue("nude_check") == "1" || r.FormValue("nude_check") == "true"
	nudeBlurReq := r.FormValue("nude_blur") == "1" || r.FormValue("nude_blur") == "true"
	blur, _ := strconv.Atoi(r.FormValue("blur"))
	doNudeBlur := NudeBlurEnabled || nudeBlurReq
	doNudeCheck := NudeCheckEnabled || nudeCheckReq || doNudeBlur

	var expiration time.Time
	if dttl := r.FormValue("draft_ttl"); dttl != "" {
		if dur, err := time.ParseDuration(dttl); err == nil {
			expiration = time.Now().Add(dur)
		} else if t, err := time.Parse("2006-01-02", dttl); err == nil {
			expiration = time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, t.Location())
		}
	}

	// Calculate Content ID for deduplication
	previewBuf := make([]byte, 512*1024)
	nBytes, err := io.ReadAtLeast(file, previewBuf, 1)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		slog.Error("Failed to read file for hashing", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	previewBuf = previewBuf[:nBytes]

	earlyHasher := sha256.New()
	earlyHasher.Write(previewBuf)
	contentID := hex.EncodeToString(earlyHasher.Sum(nil))

	contentType := header.Header.Get("Content-Type")
	// If content type is unknown, try to sniff it
	if contentType == "" || contentType == "application/octet-stream" {
		contentType = http.DetectContentType(previewBuf)
	}
	isVideo := strings.HasPrefix(contentType, "video/")

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
	if blur > 0 {
		filenameWithContentID = fmt.Sprintf("%s_blur-%d", filenameWithContentID, blur)
	}

	ext := ".png"
	if isVideo {
		ext = ".mp4"
	} else if format == "webp" {
		ext = ".webp"
	}

	// Custom save path within 'uploads'
	savePath := r.FormValue("path")
	if savePath != "" {
		savePath = filepath.Clean(savePath)
		// Prevent directory traversal
		if strings.HasPrefix(savePath, "..") || strings.HasPrefix(savePath, "/") {
			savePath = ""
		}
	}
	if savePath == "" {
		savePath = "uploads"
	} else {
		savePath = filepath.Join("uploads", savePath)
	}

	actualKey := filepath.Join(savePath, filenameWithContentID+ext)

	// Check cache
	exists, _ := GlobalStore.Exists(r.Context(), actualKey)
	draftLocal := filepath.Join(DraftPath, actualKey)
	draftExists := false
	if _, err := os.Stat(draftLocal); err == nil {
		draftExists = true
	}

	if exists || draftExists {
		slog.Info("Cache HIT for upload", "id", contentID)
		w.Header().Set("X-Cache", "HIT-ID")

		if infoReq {
			w.Header().Set("Content-Type", "application/json")
			info := map[string]interface{}{
				"is_cache_hit": true,
				"content_id":   contentID,
				"path":         actualKey,
				"is_draft":     draftExists,
				"storage_type": GlobalStore.Type(),
			}
			hashKey := actualKey + ".hashes"
			if reader, err := GlobalStore.GetReader(r.Context(), hashKey); err == nil {
				defer reader.Close()
				var hashes ImageHashes
				if err := json.NewDecoder(reader).Decode(&hashes); err == nil {
					populateInfoFromHashes(info, hashes)
				}
			}
			if draftExists {
				ttlPath := draftLocal + ".ttl"
				if ttlData, err := os.ReadFile(ttlPath); err == nil {
					if ts, err := strconv.ParseInt(string(ttlData), 10, 64); err == nil {
						info["draft_expiration"] = time.Unix(ts, 0)
					}
				}
			}
			json.NewEncoder(w).Encode(info)
			return
		}

		if draftExists {
			w.Header().Set("X-Cache", "HIT-ID-DRAFT")
			w.Header().Set("Content-Type", contentType)
			http.ServeFile(w, r, draftLocal)
			return
		}
		if localPath, isLocal := GlobalStore.LocalPath(actualKey); isLocal {
			w.Header().Set("Content-Type", contentType)
			http.ServeFile(w, r, localPath)
			return
		}
		reader, err := GlobalStore.GetReader(r.Context(), actualKey)
		if err == nil {
			defer reader.Close()
			w.Header().Set("Content-Type", contentType)
			io.Copy(w, reader)
			return
		}
	}

	fullStream := io.MultiReader(bytes.NewReader(previewBuf), file)
	mParams := MediaParams{
		Width:      width,
		Height:     height,
		Radius:     radius,
		Quality:    quality,
		Format:     format,
		CropX:      cropX,
		CropY:      cropY,
		Start:      startSec,
		End:        endSec,
		NudeCheck:  doNudeCheck,
		NudeBlur:   doNudeBlur,
		Blur:       blur,
		Info:       infoReq,
		Expiration: expiration,
	}

	processMedia(w, r, fullStream, header.Filename, actualKey, isVideo, mParams)
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

func SimilarHandler(w http.ResponseWriter, r *http.Request) {
	rawURL := r.URL.Query().Get("url")
	if rawURL == "" {
		http.Error(w, "Parameter 'url' is required", http.StatusBadRequest)
		return
	}

	threshold, _ := strconv.Atoi(r.URL.Query().Get("threshold"))
	if threshold == 0 {
		threshold = 5 // Default Hamming distance threshold
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		http.Error(w, "Invalid URL", http.StatusBadRequest)
		return
	}

	// 1. Download and get hash of the target image
	resp, err := service.DownloadStream(rawURL, r.Header)
	if err != nil {
		http.Error(w, "Failed to download image", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	img, _, err := image.Decode(resp.Body)
	if err != nil {
		http.Error(w, "Failed to decode image", http.StatusBadRequest)
		return
	}

	targetPHash, _ := goimagehash.PerceptionHash(img)

	// 2. Scan storage for similar images
	// Note: This is a naive implementation that scans the directory.
	// In production, you'd use a database with spatial indexing or a specialized vector store.
	prefix := filepath.Dir(u.Path)
	files, err := GlobalStore.List(r.Context(), prefix)
	if err != nil {
		http.Error(w, "Failed to list storage", http.StatusInternalServerError)
		return
	}

	type match struct {
		Path     string `json:"path"`
		Distance int    `json:"distance"`
	}
	var matches []match

	for _, name := range files {
		if strings.HasSuffix(name, ".hashes") {
			reader, err := GlobalStore.GetReader(r.Context(), name)
			if err != nil {
				continue
			}
			var hashes ImageHashes
			if err := json.NewDecoder(reader).Decode(&hashes); err == nil {
				pHash, err := goimagehash.ImageHashFromString(hashes.Perceptual)
				if err == nil {
					dist, _ := targetPHash.Distance(pHash)
					if dist <= threshold {
						matches = append(matches, match{
							Path:     strings.TrimSuffix(name, ".hashes"),
							Distance: dist,
						})
					}
				}
			}
			reader.Close()
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Distance < matches[j].Distance
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"url":     rawURL,
		"hash":    targetPHash.ToString(),
		"matches": matches,
	})
}
