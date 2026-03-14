package handlers

import (
	"net/http"
	"net/http/httptest" // I will add this if it was missing or fix if it was there
	"testing"
)

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
