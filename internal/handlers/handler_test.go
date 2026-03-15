package handlers

import (
	"image"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCalculateImageHashes(t *testing.T) {
	img1 := image.NewRGBA(image.Rect(0, 0, 100, 100))
	hashes := calculateImageHashes(img1)

	if hashes.Average == "" || hashes.Perceptual == "" || hashes.Difference == "" {
		t.Errorf("expected all hashes to be non-empty, got %+v", hashes)
	}
}

func TestResizeHandler_MissingURL(t *testing.T) {
	req, err := http.NewRequest("GET", "/", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(ResizeHandler)

	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusBadRequest {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusBadRequest)
	}
}

func TestResizeHandler_DomainNotAllowed(t *testing.T) {
	// Устанавливаем белый список доменов
	AllowedDomains = []string{"trusted.com"}
	defer func() { AllowedDomains = nil }()

	req, err := http.NewRequest("GET", "/?url=http://malicious.com/image.png", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(ResizeHandler)

	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusForbidden {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusForbidden)
	}
}

func TestResizeHandler_InvalidURL(t *testing.T) {
	req, err := http.NewRequest("GET", "/?url=not-a-url", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(ResizeHandler)

	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusBadRequest {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusBadRequest)
	}
}

// Примечание: тесты на успешную загрузку требуют мока внешних HTTP запросов или поднятия тестового сервера.
