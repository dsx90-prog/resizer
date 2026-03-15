package storage

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// StorageProvider defines the interface for interacting with different storage backends
type StorageProvider interface {
	Exists(ctx context.Context, path string) (bool, error)
	Save(ctx context.Context, path string, data io.Reader) error
	GetReader(ctx context.Context, path string) (io.ReadCloser, error)
	Delete(ctx context.Context, path string) error
	List(ctx context.Context, prefix string) ([]string, error)
	// Returns the type of storage ("local" or "s3")
	Type() string
	// For local-specific operations like FFmpeg processing which needs local paths
	// We might need a way to ensure a file is local
	LocalPath(path string) (string, bool)
}

// LocalStorage implements StorageProvider for the local file system
type LocalStorage struct {
	BasePath string
}

func (l *LocalStorage) Exists(ctx context.Context, path string) (bool, error) {
	fullPath := filepath.Join(l.BasePath, path)
	_, err := os.Stat(fullPath)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (l *LocalStorage) Save(ctx context.Context, path string, data io.Reader) error {
	fullPath := filepath.Join(l.BasePath, path)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	f, err := os.Create(fullPath)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, data)
	return err
}

func (l *LocalStorage) GetReader(ctx context.Context, path string) (io.ReadCloser, error) {
	fullPath := filepath.Join(l.BasePath, path)
	return os.Open(fullPath)
}

func (l *LocalStorage) Delete(ctx context.Context, path string) error {
	fullPath := filepath.Join(l.BasePath, path)
	return os.Remove(fullPath)
}

func (l *LocalStorage) List(ctx context.Context, prefix string) ([]string, error) {
	fullPath := filepath.Join(l.BasePath, prefix)
	dir := filepath.Dir(fullPath)
	base := filepath.Base(fullPath)

	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var result []string
	for _, f := range files {
		if !f.IsDir() && strings.HasPrefix(f.Name(), base) {
			result = append(result, f.Name())
		}
	}
	return result, nil
}

func (l *LocalStorage) LocalPath(path string) (string, bool) {
	return filepath.Join(l.BasePath, path), true
}

func (l *LocalStorage) Type() string {
	return "local"
}
