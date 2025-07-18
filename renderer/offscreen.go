package renderer

import (
	"fmt"
	"io"
	"log"
	"runtime"
	"time"
	"unsafe"

	gl "github.com/go-gl/gl/v4.1-core/gl"
	inputs "github.com/richinsley/goshadertoy/inputs"
	options "github.com/richinsley/goshadertoy/options"
	sharedmemory "github.com/richinsley/goshadertoy/sharedmemory"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

// #cgo CFLAGS: -I../shmframe
// #include "protocol.h"
import "C"

// Frame represents a single rendered frame's data, ready for encoding.
type Frame struct {
	Pixels []byte
	PTS    int64
}

type OffscreenRenderer struct {
	fbo               uint32
	textureID         uint32
	depthRenderbuffer uint32
	blitFbo           uint32
	blitTextureID     uint32
	width             int
	height            int
	pbos              []uint32 // Use a slice for a variable number of PBOs
	pboIndex          int      // Index to track which PBO is currently in use
	bitDepth          int
	yuvFbo            uint32
	yuvTextureIDs     [3]uint32
}

var havesetoptions = false

// getFormatForBitDepth controls the pixel format for FFmpeg readback.
func getFormatForBitDepth(bitDepth int) (glInternalFormat int32, glpixelFormat uint32, glpixelType uint32, ffmpegInPixFmt int, ffmpegOutPixFmt string) {
	// Read pixels in a planar YUV format
	ffmpegOutPixFmt = "yuv444p"

	// AV_PIX_FMT_YUV444P 		5
	// AV_PIX_FMT_YUV444P10LE	68
	// AV_PIX_FMT_YUV444P10BE	67
	switch bitDepth {
	case 10, 12:
		// For 10/12-bit, we render to a 16-bit unsigned integer texture and read back as 16-bit unsigned shorts.
		// using full 444 for encoding breaks several decoders, so we'll use 422 instead for now.
		return gl.R16UI, gl.RED_INTEGER, gl.UNSIGNED_SHORT, 68, "p010le" //"yuv444p10le" "p010le"
	default: // 8-bit
		return gl.R8UI, gl.RED_INTEGER, gl.UNSIGNED_BYTE, 5, "nv12" //"yuv444p"
	}
}
func NewOffscreenRenderer(width, height, bitDepth, numPBOs int) (*OffscreenRenderer, error) {
	if numPBOs < 2 {
		return nil, fmt.Errorf("number of PBOs must be at least 2")
	}

	or := &OffscreenRenderer{
		width:    width,
		height:   height,
		bitDepth: bitDepth,
		pbos:     make([]uint32, numPBOs*3), // 3 PBOs per frame (Y, U, V)
	}

	var internalColorFormat int32
	var colorTextureType uint32

	if bitDepth > 8 {
		log.Println("Offscreen FBO: Using 16-bit float format for HDR.")
		internalColorFormat = gl.RGBA16F
		colorTextureType = gl.FLOAT
	} else {
		log.Println("Offscreen FBO: Using 8-bit format for SDR.")
		internalColorFormat = gl.RGBA8
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
	gl.RenderbufferStorage(gl.RENDERBUFFER, gl.DEPTH_COMPONENT24, int32(width), int32(height))
	gl.FramebufferRenderbuffer(gl.FRAMEBUFFER, gl.DEPTH_ATTACHMENT, gl.RENDERBUFFER, or.depthRenderbuffer)
	if gl.CheckFramebufferStatus(gl.FRAMEBUFFER) != gl.FRAMEBUFFER_COMPLETE {
		return nil, fmt.Errorf("main offscreen fbo is not complete")
	}

	// --- Create YUV FBO for conversion ---
	gl.GenFramebuffers(1, &or.yuvFbo)
	gl.BindFramebuffer(gl.FRAMEBUFFER, or.yuvFbo)
	gl.GenTextures(3, &or.yuvTextureIDs[0])

	yuvInternalFormat, yuvPixelFormat, yuvPixelType, _, _ := getFormatForBitDepth(bitDepth)

	for i := 0; i < 3; i++ {
		gl.BindTexture(gl.TEXTURE_2D, or.yuvTextureIDs[i])
		gl.TexImage2D(gl.TEXTURE_2D, 0, yuvInternalFormat, int32(width), int32(height), 0, yuvPixelFormat, yuvPixelType, nil)
		gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.NEAREST)
		gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.NEAREST)
		gl.FramebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0+uint32(i), gl.TEXTURE_2D, or.yuvTextureIDs[i], 0)
	}

	drawBuffers := []uint32{gl.COLOR_ATTACHMENT0, gl.COLOR_ATTACHMENT1, gl.COLOR_ATTACHMENT2}
	gl.DrawBuffers(3, &drawBuffers[0])

	if gl.CheckFramebufferStatus(gl.FRAMEBUFFER) != gl.FRAMEBUFFER_COMPLETE {
		return nil, fmt.Errorf("yuv fbo is not complete")
	}

	// --- PBO Initialization ---
	gl.GenBuffers(int32(len(or.pbos)), &or.pbos[0])
	_, _, pixelType, _, _ := getFormatForBitDepth(bitDepth)
	var bytesPerPixel int
	switch pixelType {
	case gl.UNSIGNED_BYTE:
		bytesPerPixel = 1
	case gl.UNSIGNED_SHORT:
		bytesPerPixel = 2
	default:
		return nil, fmt.Errorf("unsupported pixel type for PBO sizing: %v", pixelType)
	}
	bufferSize := width * height * bytesPerPixel
	for i := 0; i < len(or.pbos); i++ {
		gl.BindBuffer(gl.PIXEL_PACK_BUFFER, or.pbos[i])
		gl.BufferData(gl.PIXEL_PACK_BUFFER, bufferSize, nil, gl.STREAM_READ)
	}

	gl.BindBuffer(gl.PIXEL_PACK_BUFFER, 0)
	gl.BindFramebuffer(gl.FRAMEBUFFER, 0)
	return or, nil
}

func (or *OffscreenRenderer) Destroy() {
	gl.DeleteFramebuffers(1, &or.fbo)
	gl.DeleteTextures(1, &or.textureID)
	gl.DeleteRenderbuffers(1, &or.depthRenderbuffer)
	gl.DeleteFramebuffers(1, &or.yuvFbo)
	gl.DeleteTextures(3, &or.yuvTextureIDs[0])
	gl.DeleteBuffers(int32(len(or.pbos)), &or.pbos[0])
}

func (or *OffscreenRenderer) readYUVPixelsAsync(width, height int) ([]byte, error) {
	_, pixelFormat, pixelType, _, _ := getFormatForBitDepth(or.bitDepth)
	var bytesPerPixel int
	switch pixelType {
	case gl.UNSIGNED_BYTE:
		bytesPerPixel = 1
	case gl.UNSIGNED_SHORT:
		bytesPerPixel = 2
	default:
		return nil, fmt.Errorf("unsupported pixel type")
	}

	bufferSize := width * height * bytesPerPixel
	yuvData := make([]byte, bufferSize*3)

	for i := 0; i < 3; i++ {
		pboIndex := (or.pboIndex + i) % len(or.pbos)
		nextPboIndex := (or.pboIndex + i + 3) % len(or.pbos)

		gl.ReadBuffer(gl.COLOR_ATTACHMENT0 + uint32(i))
		gl.BindBuffer(gl.PIXEL_PACK_BUFFER, or.pbos[pboIndex])
		gl.ReadPixels(0, 0, int32(width), int32(height), pixelFormat, pixelType, nil)

		gl.BindBuffer(gl.PIXEL_PACK_BUFFER, or.pbos[nextPboIndex])
		ptr := gl.MapBufferRange(gl.PIXEL_PACK_BUFFER, 0, bufferSize, gl.MAP_READ_BIT)
		if ptr == nil {
			gl.BindBuffer(gl.PIXEL_PACK_BUFFER, 0)
			return nil, fmt.Errorf("failed to map PBO for plane %d", i)
		}

		pixelData := (*[1 << 30]byte)(ptr)[:bufferSize:bufferSize]
		copy(yuvData[i*bufferSize:], pixelData)

		gl.UnmapBuffer(gl.PIXEL_PACK_BUFFER)
	}

	gl.BindBuffer(gl.PIXEL_PACK_BUFFER, 0)
	or.pboIndex = (or.pboIndex + 3) % len(or.pbos)

	return yuvData, nil
}

func (r *Renderer) getArgs(options *options.ShaderOptions, ffmpegOutPixFmt string) (inputArgs ffmpeg.KwArgs, outputArgs ffmpeg.KwArgs) {
	// Describe the incoming raw YUV 4:4:4 stream from the shader. This is common for all platforms.
	inputArgs = ffmpeg.KwArgs{
		"f":               "shm_demuxer",
		"color_range":     "tv",
		"colorspace":      "bt709",
		"color_primaries": "bt709",
		"color_trc":       "bt709",
	}

	outputArgs = ffmpeg.KwArgs{}

	// Platform-specific hardware acceleration logic
	switch runtime.GOOS {
	case "linux":
		log.Println("Using Linux (NVENC) hardware acceleration.")
		// The filter chain uploads the frame to the GPU, converts the pixel format, and passes it to the NVENC encoder.
		outputArgs["vf"] = fmt.Sprintf("hwupload_cuda,scale_cuda=format=%s", ffmpegOutPixFmt)
		if *options.Codec == "hevc" {
			outputArgs["c:v"] = "hevc_nvenc"
			outputArgs["preset"] = "p2" // p2, second fastest preset
		} else {
			outputArgs["c:v"] = "h264_nvenc"
			outputArgs["preset"] = "p2"
		}

	case "darwin":
		log.Println("Using macOS (VideoToolbox) hardware acceleration.")
		// The scale_vt filter handles the format conversion on the GPU before sending to the encoder.
		// Note: The 'hwupload' step is implicit with VideoToolbox filters.
		// outputArgs["vf"] = fmt.Sprintf("scale_vt=format=%s", ffmpegOutPixFmt)
		if *options.Codec == "hevc" {
			outputArgs["c:v"] = "hevc_videotoolbox"
		} else {
			outputArgs["c:v"] = "h264_videotoolbox"
		}

	default:
		log.Println("Using software encoding pipeline (no hardware acceleration).")
		// Fallback for other systems (e.g., Windows without NVENC setup)
		if *options.Codec == "hevc" {
			outputArgs["c:v"] = "libx265"
		} else {
			outputArgs["c:v"] = "libx264"
		}
	}

	// Common output arguments
	if *options.BitDepth > 8 {
		outputArgs["color_primaries"] = "bt2020"
		outputArgs["color_trc"] = "smpte2084" // PQ
		outputArgs["colorspace"] = "bt2020nc"
	}

	outputArgs["b:v"] = "25M"
	// outputArgs["pix_fmt"] = ffmpegOutPixFmt

	if *options.Codec == "hevc" && (*options.OutputFile)[len(*options.OutputFile)-4:] == ".mp4" {
		outputArgs["tag:v"] = "hvc1"
	}

	if *options.Mode == "stream" {
		outputArgs["f"] = "mpegts"
	}

	return
}

func (r *Renderer) runEncoder(options *options.ShaderOptions, frameChan <-chan *Frame, doneChan chan<- error) {
	shmNameStr := "/goshadertoy"
	bytesPerPixel := 1
	if r.offscreenRenderer.bitDepth > 8 {
		bytesPerPixel = 2
	}
	shmSize := *options.Width * *options.Height * bytesPerPixel * 3

	shm, err := sharedmemory.CreateSharedMemory(shmNameStr, shmSize)
	if err != nil {
		doneChan <- fmt.Errorf("failed to create shared memory: %w", err)
		return
	}
	defer shm.Close()

	header := C.SHMHeader{}
	shmFilePtr := (*[512]C.char)(unsafe.Pointer(&header.shm_file))
	shmFilePtr[0] = '/'
	for i := 0; i < len(shmNameStr) && i < 511; i++ {
		shmFilePtr[i+1] = C.char(shmNameStr[i])
	}
	shmFilePtr[len(shmNameStr)+1] = 0

	_, _, _, ffmpegInPixFmt, ffmpegOutPixFmt := getFormatForBitDepth(r.offscreenRenderer.bitDepth)
	header.width = C.uint32_t(*options.Width)
	header.height = C.uint32_t(*options.Height)
	header.frame_rate = C.uint32_t(*options.FPS)
	header.pix_fmt = C.int32_t(ffmpegInPixFmt)
	headerBytes := (*[unsafe.Sizeof(header)]byte)(unsafe.Pointer(&header))[:]

	pipeReader, pipeWriter := io.Pipe()
	inputArgs, outputArgs := r.getArgs(options, ffmpegOutPixFmt)

	ffmpegCmd := ffmpeg.Input("pipe:", inputArgs).
		Output(*options.OutputFile, outputArgs).
		OverWriteOutput().WithInput(pipeReader).ErrorToStdOut()

	if *options.FFMPEGPath != "" {
		ffmpegCmd = ffmpegCmd.SetFfmpegPath(*options.FFMPEGPath)
	}

	errc := make(chan error, 1)
	go func() {
		errc <- ffmpegCmd.Run()
	}()

	if _, err := pipeWriter.Write(headerBytes); err != nil {
		doneChan <- fmt.Errorf("failed to write header to FFmpeg: %w", err)
		return
	}

	ticker := time.NewTicker(time.Second / time.Duration(*options.FPS))
	defer ticker.Stop()

	var latestFrame *Frame
	var encoderPTS int64 = 0

	for {
		select {
		case <-ticker.C:
			select {
			case newFrame, ok := <-frameChan:
				if !ok {
					goto endLoop
				}
				latestFrame = newFrame
			default:
				if latestFrame == nil {
					continue
				}
			}

			if _, err := shm.WriteAt(latestFrame.Pixels, 0); err != nil {
				log.Printf("Error writing pixel data on frame %d: %v", encoderPTS, err)
				break
			}

			frameHeader := C.FrameHeader{
				cmdtype: C.uint32_t(0),
				size:    C.uint32_t(len(latestFrame.Pixels)),
				pts:     C.int64_t(encoderPTS),
			}
			encoderPTS++

			frameHeaderBytes := (*[unsafe.Sizeof(frameHeader)]byte)(unsafe.Pointer(&frameHeader))[:]
			if _, err := pipeWriter.Write(frameHeaderBytes); err != nil {
				break
			}
		case err := <-errc:
			doneChan <- err
			return
		}
	}

endLoop:
	eofHeader := C.FrameHeader{cmdtype: C.uint32_t(2)}
	eofHeaderBytes := (*[unsafe.Sizeof(eofHeader)]byte)(unsafe.Pointer(&eofHeader))[:]
	if _, err := pipeWriter.Write(eofHeaderBytes); err != nil {
		log.Printf("Error writing EOF header to FFmpeg: %v", err)
	}
	pipeWriter.Close()
	doneChan <- <-errc
}

func (r *Renderer) RunOffscreen(options *options.ShaderOptions) error {
	if *options.Mode == "stream" {
		return r.runStreamMode(options)
	}
	return r.runRecordMode(options)
}

func (r *Renderer) runStreamMode(options *options.ShaderOptions) error {
	log.Println("Starting in stream mode...")
	frameChan := make(chan *Frame, len(r.offscreenRenderer.pbos)/3)
	encoderDoneChan := make(chan error, 1)

	go r.runEncoder(options, frameChan, encoderDoneChan)

	if *options.Prewarm {
		log.Println("Pre-warming renderer...")
		for i := 0; i < len(r.offscreenRenderer.pbos); i++ {
			r.RenderFrame(0, int32(i), [4]float32{0, 0, 0, 0}, &inputs.Uniforms{})
			r.RenderToYUV()
		}
		log.Println("Pre-warming complete.")
	}

	startTime := time.Now()
	frameDuration := time.Second / time.Duration(*options.FPS)
	var frameCounter int64 = 0

	for {
		elapsedTime := time.Since(startTime)
		shouldHaveRendered := int64(float64(elapsedTime) / float64(frameDuration))

		if frameCounter >= shouldHaveRendered {
			time.Sleep(1 * time.Millisecond)
			continue
		}

		for frameCounter < shouldHaveRendered {
			simTime := float64(frameCounter) * frameDuration.Seconds()

			uniforms := &inputs.Uniforms{
				Time:      float32(simTime),
				TimeDelta: float32(frameDuration.Seconds()),
				FrameRate: float32(*options.FPS),
				Frame:     int32(frameCounter),
			}

			r.RenderFrame(simTime, int32(frameCounter), [4]float32{0, 0, 0, 0}, uniforms)
			r.RenderToYUV()

			gl.BindFramebuffer(gl.READ_FRAMEBUFFER, r.offscreenRenderer.yuvFbo)
			pixels, err := r.offscreenRenderer.readYUVPixelsAsync(*options.Width, *options.Height)
			gl.BindFramebuffer(gl.READ_FRAMEBUFFER, 0)

			if err != nil {
				log.Printf("Error reading pixels on frame %d: %v", frameCounter, err)
				close(frameChan)
				return <-encoderDoneChan
			}

			frameChan <- &Frame{Pixels: pixels, PTS: frameCounter}
			frameCounter++
		}
	}
}

func (r *Renderer) runRecordMode(options *options.ShaderOptions) error {
	log.Println("Starting in record mode...")
	totalFrames := int(*options.Duration * float64(*options.FPS))
	shmNameStr := "/goshadertoy"
	bytesPerPixel := 1
	if r.offscreenRenderer.bitDepth > 8 {
		bytesPerPixel = 2
	}
	shmSize := *options.Width * *options.Height * bytesPerPixel * 3

	shm, err := sharedmemory.CreateSharedMemory(shmNameStr, shmSize)
	if err != nil {
		return fmt.Errorf("failed to create shared memory: %w", err)
	}
	defer shm.Close()

	header := C.SHMHeader{}
	shmFilePtr := (*[512]C.char)(unsafe.Pointer(&header.shm_file))
	shmFilePtr[0] = '/'
	for i := 0; i < len(shmNameStr) && i < 511; i++ {
		shmFilePtr[i+1] = C.char(shmNameStr[i])
	}
	shmFilePtr[len(shmNameStr)+1] = 0

	_, _, _, ffmpegInPixFmt, ffmpegOutPixFmt := getFormatForBitDepth(r.offscreenRenderer.bitDepth)
	header.width = C.uint32_t(*options.Width)
	header.height = C.uint32_t(*options.Height)
	header.frame_rate = C.uint32_t(*options.FPS)
	header.pix_fmt = C.int32_t(ffmpegInPixFmt)
	headerBytes := (*[unsafe.Sizeof(header)]byte)(unsafe.Pointer(&header))[:]

	pipeReader, pipeWriter := io.Pipe()
	inputArgs, outputArgs := r.getArgs(options, ffmpegOutPixFmt)

	ffmpegCmd := ffmpeg.Input("pipe:", inputArgs).
		Output(*options.OutputFile, outputArgs).
		OverWriteOutput().WithInput(pipeReader).ErrorToStdOut()

	if *options.FFMPEGPath != "" {
		ffmpegCmd = ffmpegCmd.SetFfmpegPath(*options.FFMPEGPath)
	}

	errc := make(chan error, 1)
	go func() {
		errc <- ffmpegCmd.Run()
	}()

	if _, err := pipeWriter.Write(headerBytes); err != nil {
		return fmt.Errorf("failed to write header to FFmpeg: %w", err)
	}

	timeStep := 1.0 / float64(*options.FPS)
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
		r.RenderToYUV()

		gl.BindFramebuffer(gl.READ_FRAMEBUFFER, r.offscreenRenderer.yuvFbo)
		pixels, err := r.offscreenRenderer.readYUVPixelsAsync(*options.Width, *options.Height)
		gl.BindFramebuffer(gl.READ_FRAMEBUFFER, 0)

		if err != nil {
			break
		}

		// Write the latest pixel data to the start of the shared memory buffer.
		if _, err := shm.WriteAt(pixels, 0); err != nil {
			break
		}

		frameHeader := C.FrameHeader{
			cmdtype: C.uint32_t(0),
			size:    C.uint32_t(len(pixels)),
			pts:     C.int64_t(i),
		}
		frameHeaderBytes := (*[unsafe.Sizeof(frameHeader)]byte)(unsafe.Pointer(&frameHeader))[:]

		// Write the frame header to the pipe to signal a new frame is ready.
		if _, err := pipeWriter.Write(frameHeaderBytes); err != nil {
			break
		}
	}

	// Send the EOF command down the pipe.
	eofHeader := C.FrameHeader{cmdtype: C.uint32_t(2)}
	eofHeaderBytes := (*[unsafe.Sizeof(eofHeader)]byte)(unsafe.Pointer(&eofHeader))[:]
	pipeWriter.Write(eofHeaderBytes)
	pipeWriter.Close()
	return <-errc
}
