package service

import (
	"fmt"
	"image"
	"image/color"
	"testing"
)

func TestNudityDetection(t *testing.T) {
	err := InitONNX("../../models/nsfw_mobilenet.onnx")
	if err != nil {
		t.Fatalf("Failed to init ONNX: %v", err)
	}
	defer CloseONNX()

	// 1. Create a neutral image (Gray)
	neutralImg := image.NewRGBA(image.Rect(0, 0, 299, 299))
	for y := 0; y < 299; y++ {
		for x := 0; x < 299; x++ {
			neutralImg.Set(x, y, color.Gray{Y: 128})
		}
	}

	isNude, prob, err := IsNudeML(neutralImg, 0.5)
	if err != nil {
		t.Errorf("Detection failed: %v", err)
	}
	fmt.Printf("Neutral (Gray) image: isNude=%v, prob=%v\n", isNude, prob)

	if isNude {
		t.Error("Neutral image should not be detected as nude")
	}

	// 2. Create another neutral image (White)
	whiteImg := image.NewRGBA(image.Rect(0, 0, 299, 299))
	for y := 0; y < 299; y++ {
		for x := 0; x < 299; x++ {
			whiteImg.Set(x, y, color.White)
		}
	}
	isNude, prob, err = IsNudeML(whiteImg, 0.5)
	fmt.Printf("White image: isNude=%v, prob=%v\n", isNude, prob)
}
