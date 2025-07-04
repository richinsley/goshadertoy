// richinsley/goshadertoy/goshadertoy-7bdda94b8aa34044ba35b205c6093e66b63bf6c5/renderer/offscreen.go

package renderer

import (
	"fmt"
	"io"
	"reflect"
	"time"
	"unsafe"

	"github.com/go-gl/gl/v4.1-core/gl"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

type OffscreenRenderer struct {
	fbo       uint32
	textureID uint32
	width     int
	height    int
	pbos      [2]uint32 // For double-buffering PBOs
	pboIndex  int       // To track the current PBO
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

	// PBO INITIALIZATION
	gl.GenBuffers(2, &or.pbos[0])
	bufferSize := width * height * 4 // RGBA, 8 bits per channel (or 32 for RGBA32F)
	gl.BindBuffer(gl.PIXEL_PACK_BUFFER, or.pbos[0])
	gl.BufferData(gl.PIXEL_PACK_BUFFER, bufferSize, nil, gl.STREAM_READ)
	gl.BindBuffer(gl.PIXEL_PACK_BUFFER, or.pbos[1])
	gl.BufferData(gl.PIXEL_PACK_BUFFER, bufferSize, nil, gl.STREAM_READ)
	gl.BindBuffer(gl.PIXEL_PACK_BUFFER, 0)

	if gl.CheckFramebufferStatus(gl.FRAMEBUFFER) != gl.FRAMEBUFFER_COMPLETE {
		return nil, fmt.Errorf("offscreen framebuffer is not complete")
	}

	gl.BindFramebuffer(gl.FRAMEBUFFER, 0)
	return or, nil
}

func (or *OffscreenRenderer) Destroy() {
	gl.DeleteFramebuffers(1, &or.fbo)
	gl.DeleteTextures(1, &or.textureID)
	gl.DeleteBuffers(2, &or.pbos[0]) // Clean up the PBOs
}

// readPixelsAsync handles the asynchronous pixel transfer using two PBOs.
// It initiates the transfer for the current frame and reads the data from the previous frame.
func (or *OffscreenRenderer) readPixelsAsync(width, height int) ([]byte, error) {
	currentPboIndex := or.pboIndex
	nextPboIndex := (or.pboIndex + 1) % 2
	bufferSize := int32(width * height * 4)

	// Initiate the transfer for the CURRENT frame
	gl.BindFramebuffer(gl.FRAMEBUFFER, or.fbo)
	gl.BindBuffer(gl.PIXEL_PACK_BUFFER, or.pbos[currentPboIndex])
	gl.ReadPixels(0, 0, int32(width), int32(height), gl.RGBA, gl.UNSIGNED_BYTE, nil)

	// Read the data from the PREVIOUS frame's transfer
	gl.BindBuffer(gl.PIXEL_PACK_BUFFER, or.pbos[nextPboIndex])
	ptr := gl.MapBufferRange(gl.PIXEL_PACK_BUFFER, 0, int(bufferSize), gl.MAP_READ_BIT)
	if ptr == nil {
		return nil, fmt.Errorf("failed to map PBO")
	}

	// Create a byte slice that points to the mapped buffer
	var pixelData []byte
	header := (*reflect.SliceHeader)(unsafe.Pointer(&pixelData))
	header.Data = uintptr(ptr)
	header.Len = int(bufferSize)
	header.Cap = int(bufferSize)

	// Unmap the buffer now that we have the slice
	gl.UnmapBuffer(gl.PIXEL_PACK_BUFFER)

	// 3. Clean up and update state
	gl.BindBuffer(gl.PIXEL_PACK_BUFFER, 0)
	gl.BindFramebuffer(gl.FRAMEBUFFER, 0)

	// Update the index for the next frame
	or.pboIndex = nextPboIndex

	return pixelData, nil
}

func (r *Renderer) RunOffscreen(width, height int, duration float64, fps int, outputFile string, ffmpegPath string) error {
	pipeReader, pipeWriter := io.Pipe()

	ffmpegCmd := ffmpeg.Input("pipe:",
		ffmpeg.KwArgs{
			"format":  "rawvideo",
			"pix_fmt": "rgba", // Still RGBA as we read it from the framebuffer
			"s":       fmt.Sprintf("%dx%d", width, height),
			"r":       fmt.Sprintf("%d", fps),
		},
	).Output(outputFile,
		ffmpeg.KwArgs{
			"c:v":     "hevc_videotoolbox", // macos only
			"b:v":     "25M",               // bitrate for 4K
			"pix_fmt": "yuv444p",
		},
	).OverWriteOutput().WithInput(pipeReader).ErrorToStdOut()

	if ffmpegPath != "" {
		ffmpegCmd = ffmpegCmd.SetFfmpegPath(ffmpegPath)
	}

	errc := make(chan error, 1)
	go func() {
		errc <- ffmpegCmd.Run()
	}()

	totalFrames := int(duration * float64(fps))
	timeStep := 1.0 / float64(fps)
	startTime := time.Now()

	for i := 0; i < totalFrames; i++ {
		fmt.Printf("\rRendering frame %d of %d", i+1, totalFrames)
		currentTime := float64(i) * timeStep

		// Render the frame to the offscreen FBO
		r.RenderFrame(currentTime, int32(i), [4]float32{0, 0, 0, 0})

		// Read pixels asynchronously using our new function
		pixels, err := r.offscreenRenderer.readPixelsAsync(width, height)
		if err != nil {
			pipeWriter.Close()
			return fmt.Errorf("failed to read pixels for frame %d: %w", i, err)
		}

		// The very first frame's data will be from an empty buffer, which is fine.
		if i > 0 {
			// Write the pixel data from the *previous* frame directly to the pipe
			if _, err := pipeWriter.Write(pixels); err != nil {
				return fmt.Errorf("failed to write frame %d to pipe: %w", i, err)
			}
		}
	}

	// After the loop, we need to process the very last frame that was rendered
	// but not yet written to the pipe.
	pixels, err := r.offscreenRenderer.readPixelsAsync(width, height)
	if err == nil {
		if _, err := pipeWriter.Write(pixels); err != nil {
			fmt.Printf("Warning: failed to write last frame to pipe: %v\n", err)
		}
	} else {
		fmt.Printf("Warning: failed to read last frame: %v\n", err)
	}

	elapsed := time.Since(startTime).Seconds()
	avgFPS := float64(totalFrames) / elapsed
	fmt.Printf("\nFinished rendering %d frames in %.2f seconds (Avg: %.2f FPS)\n", totalFrames, elapsed, avgFPS)

	pipeWriter.Close()
	return <-errc
}
