package renderer

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"unsafe"

	gl "github.com/go-gl/gl/v4.1-core/gl"
	inputs "github.com/richinsley/goshadertoy/inputs"
	sharedmemory "github.com/richinsley/goshadertoy/sharedmemory"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

/*
#include "protocol.h"
*/
import "C"

type OffscreenRenderer struct {
	fbo               uint32
	textureID         uint32
	depthRenderbuffer uint32
	// Add a second FBO and texture for the vertical flip blit
	blitFbo       uint32
	blitTextureID uint32
	width         int
	height        int
	pbos          [2]uint32 // For double-buffering PBOs
	pboIndex      int       // Index to track which PBO is currently in use
	bitDepth      int
}

var havesetoptions = false

// getFormatForBitDepth controls the pixel format for FFmpeg readback.
func getFormatForBitDepth(bitDepth int) (glpixelFormat uint32, glpixelType uint32, ffmpegInPixFmt int, ffmpegOutPixFmt string) {
	// Read pixels in BGRA format to match what many video encoders expect
	glpixelFormat = gl.BGRA

	// AV_PIX_FMT_RGBA 		26
	// AV_PIX_FMT_BGRA 		28
	// AV_PIX_FMT_P010LE 	158
	// AV_PIX_FMT_BGRA64BE 	106
	// AV_PIX_FMT_BGRA64LE 	107

	switch bitDepth {
	case 10, 12:
		// For 10/12-bit, we render to a 16-bit float texture and tell FFmpeg to encode to p010le
		return glpixelFormat, gl.UNSIGNED_SHORT, 107, "p010le"
	default: // 8-bit
		return gl.BGRA, gl.UNSIGNED_BYTE, 28, "yuv420p"
	}
}

func NewOffscreenRenderer(width, height, bitDepth int) (*OffscreenRenderer, error) {
	or := &OffscreenRenderer{
		width:    width,
		height:   height,
		bitDepth: bitDepth,
	}

	var internalColorFormat int32
	var internalDepthFormat uint32
	var colorTextureType uint32

	if bitDepth > 8 {
		log.Println("Offscreen FBO: Using 16-bit float format for HDR.")
		internalColorFormat = gl.RGBA16F
		internalDepthFormat = gl.DEPTH_COMPONENT24
		colorTextureType = gl.FLOAT
	} else {
		log.Println("Offscreen FBO: Using 8-bit format for SDR.")
		internalColorFormat = gl.RGBA8
		internalDepthFormat = gl.DEPTH_COMPONENT16
		colorTextureType = gl.UNSIGNED_BYTE
	}

	// --- Create Main FBO for rendering ---
	gl.GenFramebuffers(1, &or.fbo)
	gl.BindFramebuffer(gl.FRAMEBUFFER, or.fbo)
	gl.GenTextures(1, &or.textureID)
	gl.BindTexture(gl.TEXTURE_2D, or.textureID)
	gl.TexImage2D(gl.TEXTURE_2D, 0, internalColorFormat, int32(width), int32(height), 0, gl.RGBA, colorTextureType, nil)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR)
	gl.FramebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, or.textureID, 0)
	gl.GenRenderbuffers(1, &or.depthRenderbuffer)
	gl.BindRenderbuffer(gl.RENDERBUFFER, or.depthRenderbuffer)
	gl.RenderbufferStorage(gl.RENDERBUFFER, internalDepthFormat, int32(width), int32(height))
	gl.FramebufferRenderbuffer(gl.FRAMEBUFFER, gl.DEPTH_ATTACHMENT, gl.RENDERBUFFER, or.depthRenderbuffer)
	if gl.CheckFramebufferStatus(gl.FRAMEBUFFER) != gl.FRAMEBUFFER_COMPLETE {
		return nil, fmt.Errorf("main offscreen fbo is not complete")
	}

	// --- Create Blit FBO for flipping the image ---
	gl.GenFramebuffers(1, &or.blitFbo)
	gl.BindFramebuffer(gl.FRAMEBUFFER, or.blitFbo)
	gl.GenTextures(1, &or.blitTextureID)
	gl.BindTexture(gl.TEXTURE_2D, or.blitTextureID)
	gl.TexImage2D(gl.TEXTURE_2D, 0, internalColorFormat, int32(width), int32(height), 0, gl.RGBA, colorTextureType, nil)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR)
	gl.FramebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, or.blitTextureID, 0)
	if gl.CheckFramebufferStatus(gl.FRAMEBUFFER) != gl.FRAMEBUFFER_COMPLETE {
		return nil, fmt.Errorf("blit fbo is not complete")
	}

	// --- PBO Initialization ---
	gl.GenBuffers(2, &or.pbos[0])
	_, pixelType, _, _ := getFormatForBitDepth(bitDepth)
	var bytesPerPixel int
	switch pixelType {
	case gl.UNSIGNED_BYTE:
		bytesPerPixel = 4
	case gl.UNSIGNED_SHORT, gl.HALF_FLOAT:
		bytesPerPixel = 8
	default:
		return nil, fmt.Errorf("unsupported pixel type for PBO sizing: %v", pixelType)
	}
	bufferSize := width * height * bytesPerPixel
	gl.BindBuffer(gl.PIXEL_PACK_BUFFER, or.pbos[0])
	gl.BufferData(gl.PIXEL_PACK_BUFFER, bufferSize, nil, gl.STREAM_READ)
	gl.BindBuffer(gl.PIXEL_PACK_BUFFER, or.pbos[1])
	gl.BufferData(gl.PIXEL_PACK_BUFFER, bufferSize, nil, gl.STREAM_READ)

	gl.BindBuffer(gl.PIXEL_PACK_BUFFER, 0)
	gl.BindFramebuffer(gl.FRAMEBUFFER, 0)
	return or, nil
}

func (or *OffscreenRenderer) Destroy() {
	gl.DeleteFramebuffers(1, &or.fbo)
	gl.DeleteTextures(1, &or.textureID)
	gl.DeleteRenderbuffers(1, &or.depthRenderbuffer)
	gl.DeleteFramebuffers(1, &or.blitFbo)
	gl.DeleteTextures(1, &or.blitTextureID)
	gl.DeleteBuffers(2, &or.pbos[0])
}

func (or *OffscreenRenderer) readPixelsAsync(width, height int) ([]byte, error) {
	currentPboIndex := or.pboIndex
	nextPboIndex := (or.pboIndex + 1) % 2

	pixelFormat, pixelType, _, _ := getFormatForBitDepth(or.bitDepth)
	bytesPerPixel := 4
	if pixelType == gl.HALF_FLOAT || pixelType == gl.UNSIGNED_SHORT {
		bytesPerPixel = 8
	}
	bufferSize := int32(width * height * bytesPerPixel)

	// Reads from the FBO bound to GL_READ_FRAMEBUFFER by the caller
	gl.BindBuffer(gl.PIXEL_PACK_BUFFER, or.pbos[currentPboIndex])
	gl.ReadPixels(0, 0, int32(width), int32(height), pixelFormat, pixelType, nil)

	gl.BindBuffer(gl.PIXEL_PACK_BUFFER, or.pbos[nextPboIndex])
	ptr := gl.MapBufferRange(gl.PIXEL_PACK_BUFFER, 0, int(bufferSize), gl.MAP_READ_BIT)
	if ptr == nil {
		gl.BindBuffer(gl.PIXEL_PACK_BUFFER, 0)
		return nil, fmt.Errorf("failed to map PBO")
	}

	dataCopy := make([]byte, bufferSize)
	pixelData := (*[1 << 30]byte)(ptr)[:bufferSize:bufferSize]
	copy(dataCopy, pixelData)

	gl.UnmapBuffer(gl.PIXEL_PACK_BUFFER)
	gl.BindBuffer(gl.PIXEL_PACK_BUFFER, 0)
	or.pboIndex = nextPboIndex

	return dataCopy, nil
}

func (r *Renderer) RunOffscreen(options *ShaderOptions) error {
	shmNameStr := "/goshadertoy"

	// calculate bytes per pixel based on bit depth.
	bytesPerPixel := 4 // Default for 8-bit RGBA
	if r.offscreenRenderer.bitDepth > 8 {
		bytesPerPixel = 8 // For 10/12-bit HDR (using 16-bit float textures)
	}
	frameSize := *options.Width * *options.Height * bytesPerPixel

	// The shared memory only needs to be large enough for a single frame of pixel data.
	shmSize := frameSize

	// Create shared memory using the wrapper
	shm, err := sharedmemory.CreateSharedMemory(shmNameStr, shmSize)
	if err != nil {
		return fmt.Errorf("failed to create shared memory: %w", err)
	}
	defer shm.Close()

	// --- Prepare the main SHMHeader ---
	header := C.SHMHeader{}

	// Get a pointer to the shm_file field
	shmFilePtr := (*[512]C.char)(unsafe.Pointer(&header.shm_file))

	// the shm_file needs to start with '/'
	shmFilePtr[0] = '/'

	// Copy bytes from the Go string to the C char array
	for i := 0; i < len(shmNameStr) && i < 511; i++ { // reserve 1 byte for null-terminator
		shmFilePtr[i+1] = C.char(shmNameStr[i])
	}
	shmFilePtr[len(shmNameStr)+1] = 0 // null-terminate

	_, _, ffmpegInPixFmt, ffmpegOutPixFmt := getFormatForBitDepth(r.offscreenRenderer.bitDepth)

	header.width = C.uint32_t(*options.Width)
	header.height = C.uint32_t(*options.Height)
	header.frame_rate = C.uint32_t(*options.FPS)

	header.pix_fmt = C.int32_t(ffmpegInPixFmt) // This is the pixel format for FFmpeg input that matches the GL format.
	headerBytes := (*[unsafe.Sizeof(header)]byte)(unsafe.Pointer(&header))[:]

	// --- Setup FFmpeg with a pipe for control data ---
	pipeReader, pipeWriter := io.Pipe()

	log.Printf("Recording to output file: %s with pix_fmt: %s", *options.OutputFile, ffmpegOutPixFmt)

	inputArgs := ffmpeg.KwArgs{"f": "shm_demuxer"}
	outputArgs := ffmpeg.KwArgs{
		// "c:v":     "hevc_videotoolbox",
		"c:v":     "hevc_nvenc", 
		"b:v":     "25M",
		"pix_fmt": ffmpegOutPixFmt,
		"tag:v":   "hvc1", // <= Use hvc1 for HEVC encoding for quicktime compatibility
		// "movflags": "+faststart",
	}

	// // When HDR enabled and in high bit depth mode, tag both the input and output streams correctly.
	// if r.offscreenRenderer.bitDepth > 8 {
	// 	// Tag the INPUT stream as linear light
	// 	inputArgs["color_primaries"] = "bt2020"
	// 	inputArgs["color_trc"] = "linear"
	// 	// inputArgs["colorspace"] = "bt2020"

	// 	// Tag the OUTPUT stream for HDR display
	// 	outputArgs["color_primaries"] = "bt2020"
	// 	outputArgs["color_trc"] = "smpte2084" // PQ (HDR10)
	// 	outputArgs["colorspace"] = "bt2020nc"
	// }

	log.Printf("Recording to output file: %s with pix_fmt: %s", *options.OutputFile, ffmpegOutPixFmt)
	ffmpegCmd := ffmpeg.Input("pipe:", inputArgs).
		Output(*options.OutputFile, outputArgs).
		OverWriteOutput().WithInput(pipeReader).ErrorToStdOut()

	if *options.FFMPEGPath != "" {
		ffmpegCmd = ffmpegCmd.SetFfmpegPath(*options.FFMPEGPath)
	}

	if !havesetoptions {
		ffmpeg.GlobalCommandOptions = append(ffmpeg.GlobalCommandOptions, func(cmd *exec.Cmd) {
			cmd.Env = os.Environ()
		})
		havesetoptions = true
	}

	errc := make(chan error, 1)
	go func() {
		log.Println("Starting FFmpeg...")
		errc <- ffmpegCmd.Run()
		log.Println("FFmpeg finished.")
	}()

	// Write the initial header to the pipe so the demuxer can start.
	if _, err := pipeWriter.Write(headerBytes); err != nil {
		return fmt.Errorf("failed to write header to FFmpeg: %w", err)
	}

	totalFrames := int(*options.Duration * float64(*options.FPS))
	timeStep := 1.0 / float64(*options.FPS)

	log.Printf("Starting to render %d frames...", totalFrames)
	for i := 0; i < totalFrames; i++ {
		currentTime := float64(i) * timeStep
		uniforms := &inputs.Uniforms{
			Time:      float32(currentTime),
			TimeDelta: float32(timeStep),
			FrameRate: float32(*options.FPS),
			Frame:     int32(i),
		}

		// Render the frame and get the pixel data
		r.RenderFrame(currentTime, int32(i), [4]float32{0, 0, 0, 0}, uniforms)

		// Perform the vertical flip by blitting from the main FBO to the blit FBO
		gl.BindFramebuffer(gl.FRAMEBUFFER, r.offscreenRenderer.blitFbo)
		gl.Clear(gl.COLOR_BUFFER_BIT)
		gl.UseProgram(r.blitProgram)
		gl.ActiveTexture(gl.TEXTURE0)

		// Use the output of the main render as the texture for the blit
		gl.BindTexture(gl.TEXTURE_2D, r.offscreenRenderer.textureID)
		gl.BindVertexArray(r.quadVAO)
		gl.DrawArrays(gl.TRIANGLES, 0, 6)

		// Read the pixels from the blit FBO, which now contains the flipped image
		gl.BindFramebuffer(gl.READ_FRAMEBUFFER, r.offscreenRenderer.blitFbo)
		pixels, err := r.offscreenRenderer.readPixelsAsync(*options.Width, *options.Height)
		gl.BindFramebuffer(gl.READ_FRAMEBUFFER, 0) // Unbind

		if err != nil {
			log.Printf("Error reading pixels on frame %d: %v", i, err)
			break
		}

		if len(pixels) != frameSize {
			log.Printf("Error: pixel buffer size mismatch on frame %d. Expected %d, got %d", i, frameSize, len(pixels))
			continue
		}

		// Write the latest pixel data to the start of the shared memory buffer.
		if _, err := shm.WriteAt(pixels, 0); err != nil {
			log.Printf("Error writing pixel data on frame %d: %v", i, err)
			break
		}

		// Prepare the frame header to send down the pipe.
		frameHeader := C.FrameHeader{
			cmdtype: C.uint32_t(0), // 0 for video
			size:    C.uint32_t(len(pixels)),
			pts:     C.int64_t(i),
		}
		frameHeaderBytes := (*[unsafe.Sizeof(frameHeader)]byte)(unsafe.Pointer(&frameHeader))[:]

		// Write the frame header to the pipe to signal a new frame is ready.
		if _, err := pipeWriter.Write(frameHeaderBytes); err != nil {
			log.Printf("Error writing frame header to FFmpeg on frame %d: %v", i, err)
			break
		}

		fmt.Printf("\rRendering frame %d of %d", i+1, totalFrames)
	}
	fmt.Println("\nFinished rendering frames.")

	// Send the EOF command down the pipe.
	eofHeader := C.FrameHeader{
		cmdtype: C.uint32_t(2), // 2 for EOF
		size:    0,
		pts:     0,
	}
	eofHeaderBytes := (*[unsafe.Sizeof(eofHeader)]byte)(unsafe.Pointer(&eofHeader))[:]
	if _, err := pipeWriter.Write(eofHeaderBytes); err != nil {
		log.Printf("Error writing EOF header to FFmpeg: %v", err)
	}

	log.Println("Closing FFmpeg pipe writer to signal end of stream...")
	pipeWriter.Close()

	// Wait for FFmpeg to finish processing.
	log.Println("Waiting for FFmpeg process to exit...")
	err = <-errc
	log.Println("FFmpeg process has exited.")
	return err
}
