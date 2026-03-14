package video

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// ProcessOptions parameters for ffmpeg video processing
type ProcessOptions struct {
	Width   int
	Height  int
	Quality int     // 1-100 will be mapped to CRF (18-51)
	Start   float64 // Start time in seconds
	End     float64 // End time in seconds, 0 means till the end
}

// Map quality (1-100) to CRF (18 - very good, 51 - worst).
// 100 quality -> 18 CRF, 1 quality -> 51 CRF
func qualityToCRF(q int) int {
	if q < 1 {
		q = 1
	}
	if q > 100 {
		q = 100
	}
	// Inverse proportion: (100-q)/100 * (51-18) + 18
	crf := float64(100-q)/100.0*float64(51-18) + 18
	return int(crf)
}

// getDuration returns the video duration in seconds using ffprobe
func getDuration(filePath string) (float64, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", filePath)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("ffprobe error: %w", err)
	}
	return strconv.ParseFloat(strings.TrimSpace(out.String()), 64)
}

// splitVideo cuts a large video into smaller segments without re-encoding (copy codec)
func splitVideo(ctx context.Context, inputPath string, workDir string, segmentTimeSec int, start, duration float64) ([]string, error) {
	slog.Info("Splitting video into chunks", "input", inputPath, "start", start, "duration", duration)

	segmentPattern := filepath.Join(workDir, "chunk_%04d.mp4")

	// -ss before -i for fast seeking
	args := []string{"-y"}
	if start > 0 {
		args = append(args, "-ss", strconv.FormatFloat(start, 'f', -1, 64))
	}
	if duration > 0 {
		args = append(args, "-t", strconv.FormatFloat(duration, 'f', -1, 64))
	}
	args = append(args, "-i", inputPath,
		"-c", "copy",
		"-f", "segment",
		"-segment_time", strconv.Itoa(segmentTimeSec),
		"-reset_timestamps", "1",
		segmentPattern,
	)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)

	var errOut bytes.Buffer
	cmd.Stderr = &errOut

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg split error: %v, detail: %s", err, errOut.String())
	}

	// Gather produced files
	files, err := filepath.Glob(filepath.Join(workDir, "chunk_*.mp4"))
	if err != nil {
		return nil, err
	}

	return files, nil
}

// processChunk encodes a single video chunk with filters/crf
func processChunk(ctx context.Context, inputChunk, outputChunk string, opts ProcessOptions) error {
	args := []string{
		"-y",
		"-i", inputChunk,
	}

	// Build scale filter
	if opts.Width > 0 || opts.Height > 0 {
		w := "-2"
		h := "-2"
		if opts.Width > 0 {
			w = strconv.Itoa(opts.Width)
		}
		if opts.Height > 0 {
			h = strconv.Itoa(opts.Height)
		}
		scaleFilter := fmt.Sprintf("scale=%s:%s", w, h)
		args = append(args, "-vf", scaleFilter)
	}

	// Add quality mapping to CRF (H.264 default to libx264)
	crf := qualityToCRF(opts.Quality)
	args = append(args, "-c:v", "libx264", "-crf", strconv.Itoa(crf), "-preset", "faster")

	// Trimming (only if we are NOT in a split chunk scenario where opts.Start/End would be 0)
	// Or more precisely, processChunk is the only place it can be applied to a single file.
	if opts.Start > 0 {
		args = append(args, "-ss", strconv.FormatFloat(opts.Start, 'f', -1, 64))
	}
	if opts.End > 0 {
		args = append(args, "-to", strconv.FormatFloat(opts.End, 'f', -1, 64))
	}

	// Copy audio or encode it (usually AAC is small enough, so we re-encode it just in case)
	args = append(args, "-c:a", "aac", "-b:a", "128k")

	args = append(args, outputChunk)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var errOut bytes.Buffer
	cmd.Stderr = &errOut

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg encode chunk error: %v, detail: %s", err, errOut.String())
	}

	return nil
}

// mergeChunks concatenates encoded segments back into a single video file
func mergeChunks(ctx context.Context, chunks []string, outputPath, workDir string) error {
	slog.Info("Merging chunks", "count", len(chunks))

	listFile := filepath.Join(workDir, "concat_list.txt")
	f, err := os.Create(listFile)
	if err != nil {
		return err
	}

	// File paths in concat file must be relative to the list or absolute, but escaped.
	// We'll use absolute paths formatted properly for ffmpeg list
	for _, chunk := range chunks {
		fmt.Fprintf(f, "file '%s'\n", chunk)
	}
	f.Close()

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-y",
		"-f", "concat",
		"-safe", "0",
		"-i", listFile,
		"-c", "copy", // No re-encode on merge
		outputPath,
	)

	var errOut bytes.Buffer
	cmd.Stderr = &errOut

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg concat error: %v, detail: %s", err, errOut.String())
	}

	return nil
}

// ProcessVideo is the main entrypoint: splits, encodes in parallel (goroutines), and merges
func ProcessVideo(ctx context.Context, inputPath, outputPath string, opts ProcessOptions) error {
	// Create a temporary workspace for this specific job
	workDir, err := os.MkdirTemp("", "resizer_video_*")
	if err != nil {
		return fmt.Errorf("failed to create temp workspace: %w", err)
	}
	defer os.RemoveAll(workDir) // Cleanup temp files after we are done

	// 1. Check duration
	duration, err := getDuration(inputPath)
	if err != nil {
		slog.Warn("Failed to get duration, proceeding without split check", "error", err)
		duration = 100 // fallback
	}

	// 2. Split (if video is big enough).
	// We split into 5-second chunks (configurable)
	chunkSizeSecs := 5

	// Handling trimming by adjusting duration
	actualStart := opts.Start
	actualEnd := duration
	if opts.End > 0 && opts.End < duration {
		actualEnd = opts.End
	}
	actualDuration := actualEnd - actualStart

	// If video is short (< 10s), don't split, just process as 1 chunk to avoid overhead
	if actualDuration <= float64(chunkSizeSecs*2) {
		slog.Info("Video range is short, processing as a single chunk")
		return processChunk(ctx, inputPath, outputPath, opts)
	}

	chunks, err := splitVideo(ctx, inputPath, workDir, chunkSizeSecs, actualStart, actualDuration)
	if err != nil {
		return err
	}

	if len(chunks) == 0 {
		return fmt.Errorf("splitting produced no chunks")
	}

	// 3. Process Chunks in Parallel
	// Use a worker pool (semaphore) to limit CPU consumption (e.g. max 4 chunks at a time)
	maxWorkers := 4
	sem := make(chan struct{}, maxWorkers)

	var wg sync.WaitGroup
	errCh := make(chan error, len(chunks))
	processedChunks := make([]string, len(chunks))

	for i, chunk := range chunks {
		wg.Add(1)
		go func(idx int, cPath string) {
			defer wg.Done()

			sem <- struct{}{}        // Acquire token
			defer func() { <-sem }() // Release token

			outChunk := filepath.Join(workDir, fmt.Sprintf("out_%04d.mp4", idx))

			slog.Debug("Processing chunk", "index", idx)
			// Pass options with Start/End reset to 0 because splitVideo already handled trimming
			chunkOpts := opts
			chunkOpts.Start = 0
			chunkOpts.End = 0
			if err := processChunk(ctx, cPath, outChunk, chunkOpts); err != nil {
				errCh <- err
				return
			}
			processedChunks[idx] = outChunk
		}(i, chunk)
	}

	// Wait for all processing to finish
	wg.Wait()
	close(errCh)

	// Check for any errors during processing
	if err, ok := <-errCh; ok {
		return fmt.Errorf("chunk processing failed: %w", err)
	}

	// 4. Merge Processed Chunks
	if err := mergeChunks(ctx, processedChunks, outputPath, workDir); err != nil {
		return err
	}

	slog.Info("Video processing finished", "output", outputPath)
	return nil
}

// StreamVideo reads video from input reader and writes processed/compressed video to output writer
func StreamVideo(ctx context.Context, r io.Reader, w io.Writer, opts ProcessOptions) error {
	slog.Info("Starting streaming video processing")

	// FFmpeg command for streaming
	// -i pipe:0 read from stdin
	// -f mp4 -movflags frag_keyframe+empty_moov+default_base_moof to make it streamable
	args := []string{
		"-y",
		"-i", "pipe:0",
	}

	// Build scale filter
	if opts.Width > 0 || opts.Height > 0 {
		widthStr := "-2"
		heightStr := "-2"
		if opts.Width > 0 {
			widthStr = strconv.Itoa(opts.Width)
		}
		if opts.Height > 0 {
			heightStr = strconv.Itoa(opts.Height)
		}
		args = append(args, "-vf", fmt.Sprintf("scale=%s:%s", widthStr, heightStr))
	}

	crf := qualityToCRF(opts.Quality)

	// Trimming for stream must be AFTER -i for stdin pipes
	if opts.Start > 0 {
		args = append(args, "-ss", strconv.FormatFloat(opts.Start, 'f', -1, 64))
	}
	if opts.End > 0 {
		args = append(args, "-to", strconv.FormatFloat(opts.End, 'f', -1, 64))
	}

	args = append(args,
		"-c:v", "libx264",
		"-crf", strconv.Itoa(crf),
		"-preset", "faster",
		"-c:a", "aac",
		"-b:a", "128k",
		"-f", "mp4",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"pipe:1", // write to stdout
	)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stdin = r
	cmd.Stdout = w

	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg streaming error: %v, details: %s", err, errBuf.String())
	}

	slog.Info("Streaming video processing finished")
	return nil
}
