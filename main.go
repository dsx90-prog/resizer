package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"resizer/internal/handlers"
	"resizer/internal/service"
	"resizer/pkg/storage"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server struct {
		Port string `yaml:"port"`
	} `yaml:"server"`
	Security struct {
		AllowedDomains []string `yaml:"allowed_domains"`
		Signature      struct {
			Enabled   bool   `yaml:"enabled"`
			AllowSign bool   `yaml:"allow_sign"`
			Key       string `yaml:"key"`
		} `yaml:"signature"`
		NudeCheck struct {
			Enabled    bool `yaml:"enabled"`
			FailOnNude bool `yaml:"fail_on_nude"`
		} `yaml:"nude_check"`
	} `yaml:"security"`
	Storage struct {
		Type string `yaml:"type"`
		Path string `yaml:"path"`
		S3   struct {
			Endpoint  string `yaml:"endpoint"`
			Region    string `yaml:"region"`
			AccessKey string `yaml:"access_key"`
			SecretKey string `yaml:"secret_key"`
			Bucket    string `yaml:"bucket"`
			UseSSL    bool   `yaml:"use_ssl"`
		} `yaml:"s3"`
		Download struct {
			ForwardHeaders bool              `yaml:"forward_headers"`
			UserAgent      string            `yaml:"user_agent"`
			Headers        map[string]string `yaml:"headers"`
		} `yaml:"download"`
		Draft struct {
			Enabled bool   `yaml:"enabled"`
			TTL     string `yaml:"ttl"`
		} `yaml:"draft"`
	} `yaml:"storage"`
	Video struct {
		ProcessingMode string `yaml:"processing_mode"`
	} `yaml:"video"`
	Transformations struct {
		AllowCustom bool `yaml:"allow_custom"`
		Presets     map[string]struct {
			Width   int `yaml:"width"`
			Height  int `yaml:"height"`
			Radius  int `yaml:"radius"`
			Quality int `yaml:"quality"`
		} `yaml:"presets"`
	} `yaml:"transformations"`
}

func loadConfig() *Config {
	cfg := &Config{}
	cfg.Server.Port = "8085"             // Default
	cfg.Storage.Path = "artefacts"       // Default
	cfg.Video.ProcessingMode = "chunked" // Default

	// Load from file
	f, err := os.Open("config.yml")
	if err == nil {
		defer f.Close()
		decoder := yaml.NewDecoder(f)
		if err := decoder.Decode(cfg); err != nil {
			slog.Error("Error decoding config.yml", "error", err)
		}
	}

	// Environment overrides
	if envPort := os.Getenv("PORT"); envPort != "" {
		cfg.Server.Port = envPort
	}

	if envDomains := os.Getenv("ALLOWED_DOMAINS"); envDomains != "" {
		cfg.Security.AllowedDomains = strings.Split(envDomains, ",")
		for i, d := range cfg.Security.AllowedDomains {
			cfg.Security.AllowedDomains[i] = strings.TrimSpace(d)
		}
	}

	if envStoragePath := os.Getenv("STORAGE_PATH"); envStoragePath != "" {
		cfg.Storage.Path = envStoragePath
	}

	if envVideoMode := os.Getenv("VIDEO_PROCESSING_MODE"); envVideoMode != "" {
		cfg.Video.ProcessingMode = envVideoMode
	}

	return cfg
}

func main() {
	cfg := loadConfig()

	// Инициализация логгера
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	handlers.AllowedDomains = cfg.Security.AllowedDomains
	handlers.SignatureEnabled = cfg.Security.Signature.Enabled
	handlers.AllowSignatureGen = cfg.Security.Signature.AllowSign
	handlers.SecurityKey = cfg.Security.Signature.Key
	handlers.StoragePath = cfg.Storage.Path
	handlers.NudeCheckEnabled = cfg.Security.NudeCheck.Enabled
	handlers.FailOnNude = cfg.Security.NudeCheck.FailOnNude

	// Initialize Storage Provider
	ctx := context.Background()
	if cfg.Storage.Type == "s3" {
		s3Store, err := storage.NewS3Storage(ctx,
			cfg.Storage.S3.Endpoint,
			cfg.Storage.S3.Region,
			cfg.Storage.S3.AccessKey,
			cfg.Storage.S3.SecretKey,
			cfg.Storage.S3.Bucket,
			cfg.Storage.S3.UseSSL,
		)
		if err != nil {
			slog.Error("Failed to initialize S3 storage", "error", err)
			os.Exit(1)
		}
		handlers.GlobalStore = s3Store
		slog.Info("Using S3 storage provider", "bucket", cfg.Storage.S3.Bucket)
	} else {
		handlers.GlobalStore = &storage.LocalStorage{BasePath: cfg.Storage.Path}
		slog.Info("Using local storage provider", "path", cfg.Storage.Path)
	}

	service.ForwardClientHeaders = cfg.Storage.Download.ForwardHeaders
	service.DownloadHeaders = make(map[string]string)
	if cfg.Storage.Download.UserAgent != "" {
		service.DownloadHeaders["User-Agent"] = cfg.Storage.Download.UserAgent
	}
	for k, v := range cfg.Storage.Download.Headers {
		service.DownloadHeaders[k] = v
	}

	handlers.DraftEnabled = cfg.Storage.Draft.Enabled
	if cfg.Storage.Draft.TTL != "" {
		if ttl, err := time.ParseDuration(cfg.Storage.Draft.TTL); err == nil {
			handlers.DraftTTL = ttl
		}
	}

	// Start Draft Cleanup Worker
	if handlers.DraftEnabled {
		go func() {
			ticker := time.NewTicker(10 * time.Minute)
			for range ticker.C {
				handlers.CleanupDrafts()
			}
		}()
	}

	handlers.VideoProcessingMode = cfg.Video.ProcessingMode
	handlers.AllowCustomDimensions = cfg.Transformations.AllowCustom
	handlers.Presets = make(map[string]handlers.PresetConfig)
	for name, p := range cfg.Transformations.Presets {
		handlers.Presets[name] = handlers.PresetConfig{
			Width:   p.Width,
			Height:  p.Height,
			Radius:  p.Radius,
			Quality: p.Quality,
		}
	}

	slog.Info("Starting resizer service", "port", cfg.Server.Port, "allowed_domains", handlers.AllowedDomains, "storage_path", handlers.StoragePath)

	server := &http.Server{
		Addr:    ":" + cfg.Server.Port,
		Handler: nil, // DefaultServeMux (will use http.HandleFunc mappings)
	}

	http.HandleFunc("/", handlers.ResizeHandler)
	http.HandleFunc("/check", handlers.HashCheckHandler)
	http.HandleFunc("/info", handlers.URLInfoHandler)
	http.HandleFunc("/sign", handlers.SignatureGenHandler)
	http.HandleFunc("/confirm", handlers.ConfirmHandler)
	http.HandleFunc("/similar", handlers.SimilarHandler)

	// Запуск сервера в горутине
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("Server failed", "error", err)
			os.Exit(1)
		}
	}()

	// Ожидание сигнала для gracefully завершения (Ctrl+C или SIGTERM)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("Shutting down server...")

	// Даем 10 секунд на завершение текущих запросов (скачиваний/обработки).
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		slog.Error("Server forced to shutdown", "error", err)
	}

	slog.Info("Server stopped")
}
