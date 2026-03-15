package imagep

import (
	"image"
	"image/color"
	std_draw "image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"path/filepath"
	"strings"

	"github.com/HugoSmits86/nativewebp"
	"golang.org/x/image/draw"
)

func ResizedImage(img image.Image, width, height int, cropX, cropY string) image.Image {
	srcBounds := img.Bounds()
	srcWidth := srcBounds.Dx()
	srcHeight := srcBounds.Dy()

	if width <= 0 && height <= 0 {
		return img
	}

	if width <= 0 {
		width = int(float64(height) * float64(srcWidth) / float64(srcHeight))
	} else if height <= 0 {
		height = int(float64(width) * float64(srcHeight) / float64(srcWidth))
	}

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
	ext := strings.ToLower(filepath.Ext(fileName))
	switch ext {
	case ".jpg", ".jpeg":
		return jpeg.Decode(file)
	case ".png":
		return png.Decode(file)
	case ".webp":
		return nativewebp.Decode(file)
	}

	// Fallback to standard image.Decode which uses registered sniffers
	img, _, err = image.Decode(file)
	return img, err
}

func EncodeLosslessWebP(w io.Writer, img image.Image) error {
	return nativewebp.Encode(w, img, nil)
}

// averageColor вычисляет средний цвет изображения путём сэмплинга пикселей.
func averageColor(img image.Image) color.RGBA {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	// Сэмплируем не более 64x64 пикселей для скорости
	stepX := max(1, w/64)
	stepY := max(1, h/64)

	var rSum, gSum, bSum, aSum, count uint64
	for y := bounds.Min.Y; y < bounds.Max.Y; y += stepY {
		for x := bounds.Min.X; x < bounds.Max.X; x += stepX {
			r, g, b, a := img.At(x, y).RGBA()
			rSum += uint64(r >> 8)
			gSum += uint64(g >> 8)
			bSum += uint64(b >> 8)
			aSum += uint64(a >> 8)
			count++
		}
	}
	if count == 0 {
		return color.RGBA{0, 0, 0, 255}
	}
	return color.RGBA{
		R: uint8(rSum / count),
		G: uint8(gSum / count),
		B: uint8(bSum / count),
		A: uint8(aSum / count),
	}
}

// BlurImage применяет эффект «матового стекла»:
//   - strength 1–99: многопроходное двухлинейное размытие (мягкое, без пикселизации)
//   - strength 100: замена изображения средним цветом (полная абстракция)
func BlurImage(img image.Image, strength int) image.Image {
	if strength <= 0 {
		return img
	}

	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	// При 100% — заливка средним цветом
	if strength >= 100 {
		avg := averageColor(img)
		result := image.NewRGBA(bounds)
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				result.SetRGBA(x, y, avg)
			}
		}
		return result
	}

	// Количество проходов: от 1 при strength=1 до 6 при strength=99
	passes := 1 + (strength * 5 / 99)

	// Степень сжатия: при strength=1 почти нет сжатия, при strength=99 — сильное
	// factor: от 1.05 до 20
	factor := 1.0 + (float64(strength) / 5.0)

	current := img
	for i := 0; i < passes; i++ {
		dtW := int(float64(w) / factor)
		dtH := int(float64(h) / factor)
		if dtW < 1 {
			dtW = 1
		}
		if dtH < 1 {
			dtH = 1
		}

		// Downscale
		small := image.NewRGBA(image.Rect(0, 0, dtW, dtH))
		draw.BiLinear.Scale(small, small.Bounds(), current, current.Bounds(), draw.Over, nil)

		// Upscale back to original size using CatmullRom for smoothness
		up := image.NewRGBA(bounds)
		draw.CatmullRom.Scale(up, up.Bounds(), small, small.Bounds(), draw.Over, nil)

		current = up
	}

	return current
}
