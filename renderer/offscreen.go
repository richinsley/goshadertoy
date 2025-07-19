package renderer

import (
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"time"
	"unsafe"

	gl "github.com/go-gl/gl/v4.1-core/gl"
	inputs "github.com/richinsley/goshadertoy/inputs"
	options "github.com/richinsley/goshadertoy/options"
	"github.com/richinsley/goshadertoy/semaphore"
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

const numBuffers = 3 // Use a ring buffer with 3 slots for all modes

// getFormatForBitDepth controls the pixel format for FFmpeg readback.
func getFormatForBitDepth(bitDepth int) (glInternalFormat int32, glpixelFormat uint32, glpixelType uint32, ffmpegInPixFmt int, ffmpegOutPixFmt string) {
	// Read pixels in a planar YUV format
	ffmpegOutPixFmt = "yuv444p"

	// AV_PIX_FMT_YUV444P 		5
	// AV_PIX_FMT_YUV444P10LE	68
	// AV_PIX_FMT_YUV444P10BE	67
	switch bitDepth {
	case 10, 12:
		return gl.R16UI, gl.RED_INTEGER, gl.UNSIGNED_SHORT, 68, "p010le"
	default: // 8-bit
		return gl.R8UI, gl.RED_INTEGER, gl.UNSIGNED_BYTE, 5, "nv12"
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
	inputArgs = ffmpeg.KwArgs{
		"f":               "shm_demuxer",
		"color_range":     "tv",
		"colorspace":      "bt709",
		"color_primaries": "bt709",
		"color_trc":       "bt709",
	}

	outputArgs = ffmpeg.KwArgs{}

	switch runtime.GOOS {
	case "linux":
		log.Println("Using Linux (NVENC) hardware acceleration.")
		outputArgs["vf"] = fmt.Sprintf("hwupload_cuda,scale_cuda=format=%s", ffmpegOutPixFmt)
		if *options.Codec == "hevc" {
			outputArgs["c:v"] = "hevc_nvenc"
			outputArgs["preset"] = "p2"
		} else {
			outputArgs["c:v"] = "h264_nvenc"
			outputArgs["preset"] = "p2"
		}
	case "darwin":
		log.Println("Using macOS (VideoToolbox) hardware acceleration.")
		if *options.Codec == "hevc" {
			outputArgs["c:v"] = "hevc_videotoolbox"
		} else {
			outputArgs["c:v"] = "h264_videotoolbox"
		}
	default:
		log.Println("Using software encoding pipeline (no hardware acceleration).")
		if *options.Codec == "hevc" {
			outputArgs["c:v"] = "libx265"
		} else {
			outputArgs["c:v"] = "libx264"
		}
	}

	if *options.BitDepth > 8 {
		outputArgs["color_primaries"] = "bt2020"
		outputArgs["color_trc"] = "smpte2084" // PQ
		outputArgs["colorspace"] = "bt2020nc"
	}
	outputArgs["b:v"] = "25M"

	if *options.Codec == "hevc" && (*options.OutputFile)[len(*options.OutputFile)-4:] == ".mp4" {
		outputArgs["tag:v"] = "hvc1"
	}
	if *options.Mode == "stream" {
		outputArgs["f"] = "mpegts"
	}
	return
}

// runEncoder is the Consumer. It sets up FFmpeg and consumes frames from frameChan.
func (r *Renderer) runEncoder(options *options.ShaderOptions, frameChan <-chan *Frame, doneChan chan<- error) {
	pid := os.Getpid()
	shmNameStr := fmt.Sprintf("goshadertoy_video_%d", pid)
	emptySemName := fmt.Sprintf("goshadertoy_video_empty_%d", pid)
	fullSemName := fmt.Sprintf("goshadertoy_video_full_%d", pid)

	semaphore.RemoveSemaphore(emptySemName)
	semaphore.RemoveSemaphore(fullSemName)

	bytesPerPixel := 1
	if r.offscreenRenderer.bitDepth > 8 {
		bytesPerPixel = 2
	}
	frameSize := *options.Width * *options.Height * bytesPerPixel * 3
	shmSize := int(unsafe.Sizeof(C.SHMControlBlock{})) + (frameSize * numBuffers)

	shm, err := sharedmemory.CreateSharedMemory(shmNameStr, shmSize)
	if err != nil {
		doneChan <- fmt.Errorf("failed to create shared memory: %w", err)
		return
	}
	defer shm.Close()

	emptySem, err := semaphore.NewSemaphore(emptySemName, numBuffers)
	if err != nil {
		doneChan <- fmt.Errorf("failed to create empty semaphore: %w", err)
		return
	}
	defer emptySem.Close()
	defer semaphore.RemoveSemaphore(emptySemName)

	fullSem, err := semaphore.NewSemaphore(fullSemName, 0)
	if err != nil {
		doneChan <- fmt.Errorf("failed to create full semaphore: %w", err)
		return
	}
	defer fullSem.Close()
	defer semaphore.RemoveSemaphore(fullSemName)

	controlBlockPtr := (*C.SHMControlBlock)(shm.GetPtr())
	controlBlockPtr.num_buffers = numBuffers
	controlBlockPtr.eof = 0

	var writeIndex uint32 = 0

	header := C.SHMHeader{}
	shmFilePtr := (*[512]C.char)(unsafe.Pointer(&header.shm_file))
	shmFilePtr[0] = '/'
	for i := 0; i < len(shmNameStr) && i < 511; i++ {
		shmFilePtr[i+1] = C.char(shmNameStr[i])
	}
	shmFilePtr[len(shmNameStr)+1] = 0

	emptySemNamePtr := (*[256]C.char)(unsafe.Pointer(&header.empty_sem_name))
	for i := 0; i < len(emptySemName) && i < 255; i++ {
		emptySemNamePtr[i] = C.char(emptySemName[i])
	}
	emptySemNamePtr[len(emptySemName)] = 0

	fullSemNamePtr := (*[256]C.char)(unsafe.Pointer(&header.full_sem_name))
	for i := 0; i < len(fullSemName) && i < 255; i++ {
		fullSemNamePtr[i] = C.char(fullSemName[i])
	}
	fullSemNamePtr[len(fullSemName)] = 0
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

	for frame := range frameChan {
		if err := emptySem.Acquire(); err != nil {
			log.Printf("Error acquiring empty semaphore: %v", err)
			break
		}

		writeOffset := int64(unsafe.Sizeof(C.SHMControlBlock{})) + (int64(writeIndex) * int64(frameSize))
		if _, err := shm.WriteAt(frame.Pixels, writeOffset); err != nil {
			log.Printf("Error writing pixel data on frame %d: %v", frame.PTS, err)
			break
		}

		frameHeader := C.FrameHeader{
			cmdtype: C.uint32_t(0),
			size:    C.uint32_t(len(frame.Pixels)),
			pts:     C.int64_t(frame.PTS),
			offset:  C.uint64_t(writeOffset),
		}
		frameHeaderBytes := (*[unsafe.Sizeof(frameHeader)]byte)(unsafe.Pointer(&frameHeader))[:]
		if _, err := pipeWriter.Write(frameHeaderBytes); err != nil {
			log.Printf("Error writing frame header to pipe on frame %d: %v", frame.PTS, err)
			break
		}

		writeIndex = (writeIndex + 1) % numBuffers

		if err := fullSem.Release(); err != nil {
			log.Printf("Error releasing full semaphore: %v", err)
			break
		}
	}

	controlBlockPtr.eof = 1
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

// runStreamMode is a Producer. It renders frames and sends them to the encoder.
func (r *Renderer) runStreamMode(options *options.ShaderOptions) error {
	log.Println("Starting in stream mode...")
	frameChan := make(chan *Frame, numBuffers)
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
		select {
		case err := <-encoderDoneChan:
			log.Printf("Encoder finished with error: %v", err)
			return err
		default:
		}

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

			select {
			case frameChan <- &Frame{Pixels: pixels, PTS: frameCounter}:
				frameCounter++
			default:
				log.Println("Warning: Frame channel is full. Dropping frame.")
				frameCounter++
			}
		}
	}
}

// runRecordMode is a Producer. It renders a fixed number of frames and sends them to the encoder.
func (r *Renderer) runRecordMode(options *options.ShaderOptions) error {
	log.Println("Starting in record mode...")
	frameChan := make(chan *Frame, numBuffers)
	encoderDoneChan := make(chan error, 1)

	// Start the consumer goroutine
	go r.runEncoder(options, frameChan, encoderDoneChan)

	totalFrames := int(*options.Duration * float64(*options.FPS))
	timeStep := 1.0 / float64(*options.FPS)

	for i := 0; i < totalFrames; i++ {
		currentTime := float64(i) * timeStep
		uniforms := &inputs.Uniforms{
			Time:      float32(currentTime),
			TimeDelta: float32(timeStep),
			FrameRate: float32(*options.FPS),
			Frame:     int32(i),
		}

		r.RenderFrame(currentTime, int32(i), [4]float32{0, 0, 0, 0}, uniforms)
		r.RenderToYUV()

		gl.BindFramebuffer(gl.READ_FRAMEBUFFER, r.offscreenRenderer.yuvFbo)
		pixels, err := r.offscreenRenderer.readYUVPixelsAsync(*options.Width, *options.Height)
		gl.BindFramebuffer(gl.READ_FRAMEBUFFER, 0)
		if err != nil {
			log.Printf("Error reading pixels on frame %d: %v", i, err)
			break
		}

		// Send the rendered frame to the consumer
		frameChan <- &Frame{Pixels: pixels, PTS: int64(i)}
	}

	// Close the channel to signal the producer is done
	close(frameChan)

	// Wait for the consumer to finish
	return <-encoderDoneChan
}
