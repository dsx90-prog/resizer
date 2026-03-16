package service

import (
	"bufio"
	"fmt"
	"image"
	"image/draw"
	"math"
	"os"
	"runtime"
	"sort"
	"sync"

	"github.com/nfnt/resize"
	ort "github.com/yalue/onnxruntime_go"
)

type SessionWrapper struct {
	Session     *ort.DynamicAdvancedSession
	InputShape  ort.Shape
	OutputShape ort.Shape
	InputBuffer []float32
	InputName   string
	OutputName  string
}

var (
	envOnce      sync.Once
	nsfwSession  *SessionWrapper
	classSession *SessionWrapper
	imagenetCats []string
)

func initEnv() error {
	var initErr error
	envOnce.Do(func() {
		// Detect platform for shared library
		libPath := ""
		switch runtime.GOOS {
		case "darwin":
			paths := []string{
				"/usr/local/lib/libonnxruntime.dylib",
				"/opt/homebrew/lib/libonnxruntime.dylib",
				"libonnxruntime.dylib",
			}
			for _, p := range paths {
				if _, err := os.Stat(p); err == nil {
					libPath = p
					break
				}
			}
		case "linux":
			libPath = "libonnxruntime.so"
		case "windows":
			libPath = "onnxruntime.dll"
		}

		if libPath != "" {
			ort.SetSharedLibraryPath(libPath)
		}

		err := ort.InitializeEnvironment()
		if err != nil {
			initErr = fmt.Errorf("failed to initialize ONNX environment: %v", err)
		}
	})
	return initErr
}

// InitNSFW initializes the NSFW detection model.
func InitNSFW(modelPath string) error {
	if err := initEnv(); err != nil {
		return err
	}

	session, err := ort.NewDynamicAdvancedSession(modelPath,
		[]string{"input_1"},
		[]string{"dense_3"},
		nil)
	if err != nil {
		return fmt.Errorf("failed to create NSFW session: %v", err)
	}

	nsfwSession = &SessionWrapper{
		Session:     session,
		InputShape:  ort.NewShape(1, 299, 299, 3),
		OutputShape: ort.NewShape(1, 5),
		InputBuffer: make([]float32, 1*299*299*3),
		InputName:   "input_1",
		OutputName:  "dense_3",
	}
	return nil
}

// InitClassifier initializes the general image classifier.
func InitClassifier(modelPath, labelsPath string) error {
	if err := initEnv(); err != nil {
		return err
	}

	// Load labels
	f, err := os.Open(labelsPath)
	if err != nil {
		return fmt.Errorf("failed to open labels file: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		imagenetCats = append(imagenetCats, scanner.Text())
	}

	// MobileNetV2 from ONNX Model Zoo usually has input "input" and output "output"
	// but let's check or assume standard for mobilenetv2-10
	session, err := ort.NewDynamicAdvancedSession(modelPath,
		[]string{"input"},
		[]string{"output"},
		nil)
	if err != nil {
		// Try alternative names if "input"/"output" fails
		session, err = ort.NewDynamicAdvancedSession(modelPath,
			[]string{"data"},
			[]string{"mobilenetv20_output_flatten0_reshape0"},
			nil)
		if err != nil {
			return fmt.Errorf("failed to create classifier session: %v", err)
		}
	}

	classSession = &SessionWrapper{
		Session:     session,
		InputShape:  ort.NewShape(1, 3, 224, 224), // MobileNetV2 usually uses NCHW 224x224
		OutputShape: ort.NewShape(1, 1000),
		InputBuffer: make([]float32, 1*3*224*224),
		InputName:   "input",
		OutputName:  "output",
	}
	return nil
}

// CloseONNX releases ONNX resources.
func CloseONNX() {
	if nsfwSession != nil {
		nsfwSession.Session.Destroy()
	}
	if classSession != nil {
		classSession.Session.Destroy()
	}
	ort.DestroyEnvironment()
}

// IsNudeML checks if the image contains nudity using the ML model.
func IsNudeML(img image.Image, threshold float64) (bool, map[string]float64, error) {
	if nsfwSession == nil {
		return false, nil, fmt.Errorf("NSFW session not initialized")
	}

	resized := resize.Resize(299, 299, img, resize.Bilinear)
	bounds := resized.Bounds()
	rgba := image.NewRGBA(bounds)
	draw.Draw(rgba, bounds, resized, bounds.Min, draw.Src)

	for y := 0; y < 299; y++ {
		for x := 0; x < 299; x++ {
			r, g, b, _ := rgba.At(x, y).RGBA()
			idx := (y*299 + x) * 3
			nsfwSession.InputBuffer[idx] = (float32(r>>8) / 127.5) - 1.0
			nsfwSession.InputBuffer[idx+1] = (float32(g>>8) / 127.5) - 1.0
			nsfwSession.InputBuffer[idx+2] = (float32(b>>8) / 127.5) - 1.0
		}
	}

	inputTensor, _ := ort.NewTensor(nsfwSession.InputShape, nsfwSession.InputBuffer)
	defer inputTensor.Destroy()
	outputTensor, _ := ort.NewEmptyTensor[float32](nsfwSession.OutputShape)
	defer outputTensor.Destroy()

	err := nsfwSession.Session.Run([]ort.ArbitraryTensor{inputTensor}, []ort.ArbitraryTensor{outputTensor})
	if err != nil {
		return false, nil, err
	}

	results := outputTensor.GetData()
	probs := map[string]float64{
		"Drawing": float64(results[0]),
		"Hentai":  float64(results[1]),
		"Neutral": float64(results[2]),
		"Porn":    float64(results[3]),
		"Sexy":    float64(results[4]),
	}
	nudeProb := probs["Porn"] + probs["Hentai"]
	return nudeProb > threshold, probs, nil
}

type Classification struct {
	Label       string  `json:"label"`
	Probability float64 `json:"probability"`
}

func softmax(logits []float32) []float64 {
	maxLogit := float32(-1e30)
	for _, v := range logits {
		if v > maxLogit {
			maxLogit = v
		}
	}

	var sum float64
	probs := make([]float64, len(logits))
	for i, v := range logits {
		probs[i] = math.Exp(float64(v - maxLogit))
		sum += probs[i]
	}

	for i := range probs {
		probs[i] /= sum
	}
	return probs
}

// ClassifyImage recognizes objects in the image.
func ClassifyImage(img image.Image, threshold float64) ([]Classification, error) {
	if classSession == nil {
		return nil, fmt.Errorf("Classifier session not initialized")
	}

	// 1. Preprocess: Resize to 224x224 (NCHW)
	resized := resize.Resize(224, 224, img, resize.Bilinear)
	rgba := image.NewRGBA(resized.Bounds())
	draw.Draw(rgba, rgba.Bounds(), resized, resized.Bounds().Min, draw.Src)

	// ImageNet normalization: mean=[0.485, 0.456, 0.406], std=[0.229, 0.224, 0.225]
	for y := 0; y < 224; y++ {
		for x := 0; x < 224; x++ {
			r, g, b, _ := rgba.At(x, y).RGBA()
			rf := float32(r>>8) / 255.0
			gf := float32(g>>8) / 255.0
			bf := float32(b>>8) / 255.0

			// NCHW format
			classSession.InputBuffer[0*224*224+y*224+x] = (rf - 0.485) / 0.229
			classSession.InputBuffer[1*224*224+y*224+x] = (gf - 0.456) / 0.224
			classSession.InputBuffer[2*224*224+y*224+x] = (bf - 0.406) / 0.225
		}
	}

	inputTensor, _ := ort.NewTensor(classSession.InputShape, classSession.InputBuffer)
	defer inputTensor.Destroy()
	outputTensor, _ := ort.NewEmptyTensor[float32](classSession.OutputShape)
	defer outputTensor.Destroy()

	err := classSession.Session.Run([]ort.ArbitraryTensor{inputTensor}, []ort.ArbitraryTensor{outputTensor})
	if err != nil {
		return nil, err
	}

	probs := softmax(outputTensor.GetData())
	var classes []Classification
	for i, prob := range probs {
		if prob >= threshold {
			label := fmt.Sprintf("class_%d", i)
			if i < len(imagenetCats) {
				label = imagenetCats[i]
			}
			classes = append(classes, Classification{Label: label, Probability: prob})
		}
	}

	sort.Slice(classes, func(i, j int) bool {
		return classes[i].Probability > classes[j].Probability
	})

	if len(classes) > 5 {
		classes = classes[:5]
	}

	return classes, nil
}
