package imagep

import (
	"image"
	"testing"
)

func TestResizedImage(t *testing.T) {
	// Создаем тестовое изображение 100x100
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))

	targetWidth := 50
	targetHeight := 50

	resized := ResizedImage(img, targetWidth, targetHeight, "center", "center")

	if resized.Bounds().Dx() != targetWidth {
		t.Errorf("expected width %d, got %d", targetWidth, resized.Bounds().Dx())
	}

	if resized.Bounds().Dy() != targetHeight {
		t.Errorf("expected height %d, got %d", targetHeight, resized.Bounds().Dy())
	}
}

func TestRoundImage(t *testing.T) {
	// Создаем тестовое изображение 100x100
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))

	radius := 50
	rounded := RoundImage(img, radius)

	// Проверяем размеры (они не должны меняться)
	if rounded.Bounds().Dx() != 100 {
		t.Errorf("expected width %d, got %d", 100, rounded.Bounds().Dx())
	}

	// В данном случае мы проверяем именно факт работы функции без паники
	// Для глубокой проверки нужно анализировать пиксели на прозрачность
}

func BenchmarkResizedImage(b *testing.B) {
	img := image.NewRGBA(image.Rect(0, 0, 1000, 1000))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ResizedImage(img, 500, 500, "center", "center")
	}
}

func BenchmarkRoundImage(b *testing.B) {
	img := image.NewRGBA(image.Rect(0, 0, 1000, 1000))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = RoundImage(img, 500)
	}
}
