package renderer

import (
	"fmt"
	"io"
	"log"
	"time"

	"github.com/go-gl/gl/v4.1-core/gl"
	inputs "github.com/richinsley/goshadertoy/inputs"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

type OffscreenRenderer struct {
	fbo       uint32
	textureID uint32
	width     int
	height    int
	pbos      [2]uint32 // For double-buffering PBOs
	pboIndex  int       // To track the current PBO
	bitDepth  int
}

// getFormatForBitDepth returns the appropriate OpenGL and FFmpeg formats for a given bit depth.
func getFormatForBitDepth(bitDepth int) (internalFormat int32, pixelFormat uint32, pixelType uint32, ffmpegInPixFmt string, ffmpegOutPixFmt string) {
	switch bitDepth {
	case 10, 12:
		// For 10 or 12-bit output, we render to a 16-bit FBO.
		// We read pixels as 16-bit unsigned shorts per channel (8 bytes/pixel).
		// This matches FFmpeg's `rgba64le` (RGBA, 64-bit, little-endian) format.
		// The encoder then converts this to a 10-bit YUV format.
		return gl.RGBA16F, gl.RGBA, gl.UNSIGNED_SHORT, "rgba64le", "p010le"
	default: // 8-bit
		// Standard 8-bit RGBA, output as standard 8-bit 4:2:0 YUV.
		return gl.RGBA8, gl.RGBA, gl.UNSIGNED_BYTE, "rgba", "yuv420p"
	}
}

func NewOffscreenRenderer(width, height, bitDepth int) (*OffscreenRenderer, error) {
	or := &OffscreenRenderer{
		width:    width,
		height:   height,
		bitDepth: bitDepth,
	}

	internalFormat, _, _, _, _ := getFormatForBitDepth(bitDepth)

	gl.GenFramebuffers(1, &or.fbo)
	gl.BindFramebuffer(gl.FRAMEBUFFER, or.fbo)

	gl.GenTextures(1, &or.textureID)
	gl.BindTexture(gl.TEXTURE_2D, or.textureID)
	// Set the FBO's texture to the correct internal format for the chosen bit depth.
	gl.TexImage2D(gl.TEXTURE_2D, 0, internalFormat, int32(width), int32(height), 0, gl.RGBA, gl.FLOAT, nil)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR)
	gl.FramebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, or.textureID, 0)

	// PBO INITIALIZATION
	gl.GenBuffers(2, &or.pbos[0])
	bytesPerPixel := 4 // Default for 8-bit RGBA
	if bitDepth > 8 {
		bytesPerPixel = 8 // for 10/12 bit (packed into 64-bit RGBA)
	}
	bufferSize := width * height * bytesPerPixel
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
func (or *OffscreenRenderer) readPixelsAsync(width, height int) ([]byte, error) {
	currentPboIndex := or.pboIndex
	nextPboIndex := (or.pboIndex + 1) % 2

	bytesPerPixel := 4
	if or.bitDepth > 8 {
		bytesPerPixel = 8
	}
	bufferSize := int32(width * height * bytesPerPixel)

	_, pixelFormat, pixelType, _, _ := getFormatForBitDepth(or.bitDepth)

	// Initiate the transfer for the CURRENT frame
	gl.BindFramebuffer(gl.FRAMEBUFFER, or.fbo)
	gl.BindBuffer(gl.PIXEL_PACK_BUFFER, or.pbos[currentPboIndex])
	gl.ReadPixels(0, 0, int32(width), int32(height), pixelFormat, pixelType, nil)

	// Read the data from the PREVIOUS frame's transfer
	gl.BindBuffer(gl.PIXEL_PACK_BUFFER, or.pbos[nextPboIndex])
	ptr := gl.MapBufferRange(gl.PIXEL_PACK_BUFFER, 0, int(bufferSize), gl.MAP_READ_BIT)
	if ptr == nil {
		gl.BindBuffer(gl.PIXEL_PACK_BUFFER, 0)
		return nil, fmt.Errorf("failed to map PBO")
	}

	// It's crucial to copy the data out of the mapped buffer before unmapping it.
	// We create a new slice and copy the data into it.
	dataCopy := make([]byte, bufferSize)
	pixelData := (*[1 << 30]byte)(ptr)[:bufferSize:bufferSize]
	copy(dataCopy, pixelData)

	// Unmap the buffer now that we have the slice
	gl.UnmapBuffer(gl.PIXEL_PACK_BUFFER)

	// Clean up and update state
	gl.BindBuffer(gl.PIXEL_PACK_BUFFER, 0)
	gl.BindFramebuffer(gl.FRAMEBUFFER, 0)

	// Update the index for the next frame
	or.pboIndex = nextPboIndex

	return dataCopy, nil
}

func (r *Renderer) RunOffscreen(options *ShaderOptions) error {
	pipeReader, pipeWriter := io.Pipe()

	_, _, _, ffmpegInPixFmt, ffmpegOutPixFmt := getFormatForBitDepth(*options.BitDepth)

	var ffmpegCmd *ffmpeg.Stream

	inputArgs := ffmpeg.KwArgs{
		"format":  "rawvideo",
		"pix_fmt": ffmpegInPixFmt,
		"s":       fmt.Sprintf("%dx%d", *options.Width, *options.Height),
		"r":       fmt.Sprintf("%d", *options.FPS),
	}

	if *options.DecklinkDevice != "" && len(*options.DecklinkDevice) > 0 {
		log.Printf("Streaming to DeckLink device: %s", *options.DecklinkDevice)
		inputArgs["re"] = ""
		ffmpegCmd = ffmpeg.Input("pipe:", inputArgs).
			Output(*options.DecklinkDevice,
				ffmpeg.KwArgs{
					"format":  "decklink",
					"pix_fmt": "uyvy422",
				},
			).WithInput(pipeReader).ErrorToStdOut()

	} else {
		log.Printf("Recording to output file: %s", *options.OutputFile)
		ffmpegCmd = ffmpeg.Input("pipe:", inputArgs).
			Output(*options.OutputFile,
				ffmpeg.KwArgs{
					"c:v":     "hevc_videotoolbox",
					"b:v":     "25M",
					"pix_fmt": ffmpegOutPixFmt,
				},
			).OverWriteOutput().WithInput(pipeReader).ErrorToStdOut()
	}

	if *options.FFMPEGPath != "" {
		ffmpegCmd = ffmpegCmd.SetFfmpegPath(*options.FFMPEGPath)
	}

	errc := make(chan error, 1)
	go func() {
		errc <- ffmpegCmd.Run()
	}()

	totalFrames := int(*options.Duration * float64(*options.FPS))
	timeStep := 1.0 / float64(*options.FPS)
	startTime := time.Now()

	// Dummy uniforms for the first frame
	dummyUniforms := &inputs.Uniforms{
		Time:      0,
		TimeDelta: 0,
		FrameRate: float32(*options.FPS),
		Frame:     -1,
		Mouse:     [4]float32{0, 0, 0, 0},
		ChannelTime: [4]float32{
			0, 0, 0, 0,
		},
		SampleRate: 44100,
	}

	r.RenderFrame(0, -1, [4]float32{0, 0, 0, 0}, dummyUniforms)
	r.offscreenRenderer.readPixelsAsync(*options.Width, *options.Height)

	var i int
	for {
		if *options.Duration > 0 && i >= totalFrames {
			break
		}

		if *options.Duration > 0 {
			fmt.Printf("\rRendering frame %d of %d", i+1, totalFrames)
		} else {
			fmt.Printf("\rRendering frame %d", i+1)
		}

		currentTime := float64(i) * timeStep
		timeDelta := float32(timeStep)

		uniforms := &inputs.Uniforms{
			Time:      float32(currentTime),
			TimeDelta: timeDelta,
			FrameRate: float32(*options.FPS),
			Frame:     int32(i),
			Mouse:     [4]float32{0, 0, 0, 0},
			ChannelTime: [4]float32{
				float32(currentTime),
				float32(currentTime),
				float32(currentTime),
				float32(currentTime),
			},
			SampleRate: 44100,
		}

		r.RenderFrame(currentTime, int32(i), [4]float32{0, 0, 0, 0}, uniforms)

		pixels, err := r.offscreenRenderer.readPixelsAsync(*options.Width, *options.Height)
		if err != nil {
			pipeWriter.Close()
			return fmt.Errorf("failed to read pixels for frame %d: %w", i, err)
		}

		if _, err := pipeWriter.Write(pixels); err != nil {
			break
		}

		i++
	}

	elapsed := time.Since(startTime).Seconds()
	avgFPS := float64(totalFrames) / elapsed
	fmt.Printf("\nFinished rendering %d frames in %.2f seconds (Avg: %.2f FPS)\n", totalFrames, elapsed, avgFPS)

	pipeWriter.Close()
	return <-errc
}
