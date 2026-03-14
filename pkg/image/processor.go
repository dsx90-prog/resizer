package imagep

import (
	"fmt"
	"image"
	"image/color"
	std_draw "image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"path/filepath"
	"strings"

	"golang.org/x/image/draw"
)

func ResizedImage(img image.Image, width, height int, cropX, cropY string) image.Image {
	srcBounds := img.Bounds()
	srcWidth := srcBounds.Dx()
	srcHeight := srcBounds.Dy()

	// Вычисляем коэффициенты масштабирования для обеих осей
	scaleX := float64(width) / float64(srcWidth)
	scaleY := float64(height) / float64(srcHeight)

	// Выбираем максимальный коэффициент, чтобы изображение полностью заполнило целевую область (Fill)
	scale := scaleX
	if scaleY > scale {
		scale = scaleY
	}

	// Новые размеры изображения с сохранением пропорций (до обрезки)
	interWidth := int(float64(srcWidth) * scale)
	interHeight := int(float64(srcHeight) * scale)

	// Создаем промежуточное масштабированное изображение
	interImg := image.NewRGBA(image.Rect(0, 0, interWidth, interHeight))
	draw.BiLinear.Scale(interImg, interImg.Bounds(), img, srcBounds, draw.Over, nil)

	// Вычисляем координаты для обрезки из промежуточного изображения
	var startX, startY int

	switch strings.ToLower(cropX) {
	case "left":
		startX = 0
	case "right":
		startX = interWidth - width
	default: // center
		startX = (interWidth - width) / 2
	}

	switch strings.ToLower(cropY) {
	case "top":
		startY = 0
	case "bottom":
		startY = interHeight - height
	default: // center
		startY = (interHeight - height) / 2
	}

	resultImg := image.NewRGBA(image.Rect(0, 0, width, height))

	// Копируем нужную часть (обрезаем) из interImg в resultImg.
	std_draw.Draw(resultImg, resultImg.Bounds(), interImg, image.Point{X: startX, Y: startY}, std_draw.Src)

	return resultImg
}

// roundedMask implements image.Image for drawing rounded corners
type roundedMask struct {
	rect   image.Rectangle
	radius int
}

func (m *roundedMask) ColorModel() color.Model {
	return color.AlphaModel
}

func (m *roundedMask) Bounds() image.Rectangle {
	return m.rect
}

func (m *roundedMask) At(x, y int) color.Color {
	r := m.radius
	// If radius is 0 or less, no rounding
	if r <= 0 {
		return color.Alpha{255}
	}

	w := m.rect.Dx()
	h := m.rect.Dy()

	// Coordinates relative to the top-left corner (0,0)
	rx := x - m.rect.Min.X
	ry := y - m.rect.Min.Y

	// Check the 4 corners
	// Top-left
	if rx < r && ry < r {
		dx := float64(rx - r + 1)
		dy := float64(ry - r + 1) // Offset by 1 for center of pixel
		if dx*dx+dy*dy > float64(r*r) {
			return color.Alpha{0}
		}
	}
	// Top-right
	if rx >= w-r && ry < r {
		dx := float64(rx - (w - r))
		dy := float64(ry - r + 1)
		if dx*dx+dy*dy > float64(r*r) {
			return color.Alpha{0}
		}
	}
	// Bottom-left
	if rx < r && ry >= h-r {
		dx := float64(rx - r + 1)
		dy := float64(ry - (h - r))
		if dx*dx+dy*dy > float64(r*r) {
			return color.Alpha{0}
		}
	}
	// Bottom-right
	if rx >= w-r && ry >= h-r {
		dx := float64(rx - (w - r))
		dy := float64(ry - (h - r))
		if dx*dx+dy*dy > float64(r*r) {
			return color.Alpha{0}
		}
	}

	// Inside the rounded rectangle
	return color.Alpha{255}
}

func RoundImage(img image.Image, radius int) image.Image {
	bounds := img.Bounds()

	// Максимальный возможный радиус скругления для данного размера (овал/круг)
	maxRadius := min(bounds.Dx(), bounds.Dy()) / 2

	// Переданный radius теперь интерпретируется как проценты (от 0 до 100).
	// Вычисляем абсолютный радиус в пикселях.
	actualRadius := (maxRadius * radius) / 100

	if actualRadius > maxRadius {
		actualRadius = maxRadius
	}

	result := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))

	mask := &roundedMask{
		rect:   bounds,
		radius: actualRadius,
	}

	std_draw.DrawMask(
		result,
		result.Bounds(),
		img,
		bounds.Min,
		mask,
		bounds.Min,
		std_draw.Over,
	)

	return result
}

func DecodeImage(fileName string, file io.Reader) (img image.Image, err error) {
	switch strings.ToLower(filepath.Ext(fileName)) {
	case ".jpg", ".jpeg":
		return jpeg.Decode(file)
	case ".png":
		return png.Decode(file)
	default:
		return nil, fmt.Errorf("unsupported file format")
	}
}
