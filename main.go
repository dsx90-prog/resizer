package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"resizer/internal/handlers"
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
	} `yaml:"security"`
	Storage struct {
		Path string `yaml:"path"`
	} `yaml:"storage"`
}

func loadConfig() *Config {
	cfg := &Config{}
	cfg.Server.Port = "8085"       // Default
	cfg.Storage.Path = "artefacts" // Default

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

	return cfg
}

func main() {
	cfg := loadConfig()

	// Инициализация логгера
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	handlers.AllowedDomains = cfg.Security.AllowedDomains
	handlers.StoragePath = cfg.Storage.Path

	slog.Info("Starting resizer service", "port", cfg.Server.Port, "allowed_domains", handlers.AllowedDomains, "storage_path", handlers.StoragePath)

	server := &http.Server{
		Addr:    ":" + cfg.Server.Port,
		Handler: nil, // DefaultServeMux (will use http.HandleFunc mappings)
	}

	http.HandleFunc("/", handlers.ResizeHandler)

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
