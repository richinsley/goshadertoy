package renderer

import (
	"fmt"
	"io"
	"time"

	"github.com/go-gl/gl/v4.1-core/gl"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

type OffscreenRenderer struct {
	fbo       uint32
	textureID uint32
	width     int
	height    int
}

func NewOffscreenRenderer(width, height int) (*OffscreenRenderer, error) {
	or := &OffscreenRenderer{
		width:  width,
		height: height,
	}

	gl.GenFramebuffers(1, &or.fbo)
	gl.BindFramebuffer(gl.FRAMEBUFFER, or.fbo)

	gl.GenTextures(1, &or.textureID)
	gl.BindTexture(gl.TEXTURE_2D, or.textureID)
	gl.TexImage2D(gl.TEXTURE_2D, 0, gl.RGBA32F, int32(width), int32(height), 0, gl.RGBA, gl.FLOAT, nil)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR)
	gl.FramebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, or.textureID, 0)

	if gl.CheckFramebufferStatus(gl.FRAMEBUFFER) != gl.FRAMEBUFFER_COMPLETE {
		return nil, fmt.Errorf("offscreen framebuffer is not complete")
	}

	gl.BindFramebuffer(gl.FRAMEBUFFER, 0)
	return or, nil
}

func (or *OffscreenRenderer) Destroy() {
	gl.DeleteFramebuffers(1, &or.fbo)
	gl.DeleteTextures(1, &or.textureID)
}

func (r *Renderer) RunOffscreen(width, height int, duration float64, fps int, outputFile string) error {
	pipeReader, pipeWriter := io.Pipe()

	ffmpegCmd := ffmpeg.Input("pipe:",
		ffmpeg.KwArgs{
			"format":  "rawvideo",
			"pix_fmt": "rgba",
			"s":       fmt.Sprintf("%dx%d", width, height),
			"r":       fmt.Sprintf("%d", fps),
		},
	).Output(outputFile,
		ffmpeg.KwArgs{
			// "c:v":     "libx264",
			// "preset":  "ultrafast",
			"c:v":     "h264_videotoolbox",
			"pix_fmt": "yuv420p",
		},
	).OverWriteOutput().WithInput(pipeReader).ErrorToStdOut() // <- uncomment for ffmpeg debug output

	errc := make(chan error, 1)
	go func() {
		errc <- ffmpegCmd.Run()
	}()

	totalFrames := int(duration * float64(fps))
	timeStep := 1.0 / float64(fps)
	startTime := time.Now()

	for i := 0; i < totalFrames; i++ {
		// Print progress to the console
		fmt.Printf("\rRendering frame %d of %d", i+1, totalFrames)

		currentTime := float64(i) * timeStep
		r.RenderFrame(currentTime, int32(i), [4]float32{0, 0, 0, 0})

		pixels, err := r.readPixels()
		if err != nil {
			pipeWriter.Close()
			return fmt.Errorf("failed to read pixels for frame %d: %w", i, err)
		}

		// Vertically flip the image and write to the pipe
		rowSize := width * 4
		for y := height - 1; y >= 0; y-- {
			start := y * rowSize
			end := start + rowSize
			if _, err := pipeWriter.Write(pixels[start:end]); err != nil {
				return fmt.Errorf("failed to write frame %d to pipe: %w", i, err)
			}
		}
	}

	// Calculate and print the final performance stats
	elapsed := time.Since(startTime).Seconds()
	avgFPS := float64(totalFrames) / elapsed
	fmt.Printf("\nFinished rendering %d frames in %.2f seconds (Avg: %.2f FPS)\n", totalFrames, elapsed, avgFPS)

	pipeWriter.Close()
	return <-errc
}

func (r *Renderer) readPixels() ([]byte, error) {
	width, height := r.offscreenRenderer.width, r.offscreenRenderer.height
	size := width * height * 4
	pixels := make([]byte, size)

	gl.BindFramebuffer(gl.FRAMEBUFFER, r.offscreenRenderer.fbo)
	gl.ReadPixels(0, 0, int32(width), int32(height), gl.RGBA, gl.UNSIGNED_BYTE, gl.Ptr(pixels))
	gl.BindFramebuffer(gl.FRAMEBUFFER, 0)

	return pixels, nil
}
