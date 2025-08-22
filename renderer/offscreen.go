package renderer

import (
	"fmt"
	"log"
	"time"

	gl "github.com/go-gl/gl/v4.1-core/gl"
	"github.com/richinsley/goshadertoy/audio"
	"github.com/richinsley/goshadertoy/encoder"
	"github.com/richinsley/goshadertoy/inputs"
	"github.com/richinsley/goshadertoy/options"
)

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

// getFormatForBitDepth controls the pixel format for readback.
// The output is now always planar YUV.
func getFormatForBitDepth(bitDepth int) (glInternalFormat int32, glpixelFormat uint32, glpixelType uint32) {
	switch bitDepth {
	case 10, 12:
		return gl.R16UI, gl.RED_INTEGER, gl.UNSIGNED_SHORT
	default: // 8-bit
		return gl.R8UI, gl.RED_INTEGER, gl.UNSIGNED_BYTE
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

	// Create Main FBO for rendering
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

	// Create YUV FBO for conversion
	gl.GenFramebuffers(1, &or.yuvFbo)
	gl.BindFramebuffer(gl.FRAMEBUFFER, or.yuvFbo)
	gl.GenTextures(3, &or.yuvTextureIDs[0])

	yuvInternalFormat, yuvPixelFormat, yuvPixelType := getFormatForBitDepth(bitDepth)

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

	// PBO Initialization
	gl.GenBuffers(int32(len(or.pbos)), &or.pbos[0])
	_, _, pixelType := getFormatForBitDepth(bitDepth)
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
	_, pixelFormat, pixelType := getFormatForBitDepth(or.bitDepth)
	var bytesPerPixel int
	switch pixelType {
	case gl.UNSIGNED_BYTE:
		bytesPerPixel = 1
	case gl.UNSIGNED_SHORT:
		bytesPerPixel = 2
	default:
		return nil, fmt.Errorf("unsupported pixel type")
	}

	planeSize := width * height * bytesPerPixel
	yuvData := make([]byte, planeSize*3) // Y, U, V planes concatenated

	// This logic implements triple-buffering with PBOs to avoid stalling the pipeline.
	for i := 0; i < 3; i++ { // For each plane Y, U, V
		currentPboIndex := (or.pboIndex + i) % len(or.pbos)
		nextPboIndex := (or.pboIndex + i + 3) % len(or.pbos)

		// 1. Issue read command for the current frame into the current PBO
		gl.ReadBuffer(gl.COLOR_ATTACHMENT0 + uint32(i))
		gl.BindBuffer(gl.PIXEL_PACK_BUFFER, or.pbos[currentPboIndex])
		gl.ReadPixels(0, 0, int32(width), int32(height), pixelFormat, pixelType, nil)

		// 2. Process the data from the *previous* frame's PBO (which should be ready now)
		gl.BindBuffer(gl.PIXEL_PACK_BUFFER, or.pbos[nextPboIndex])
		ptr := gl.MapBufferRange(gl.PIXEL_PACK_BUFFER, 0, planeSize, gl.MAP_READ_BIT)
		if ptr == nil {
			gl.BindBuffer(gl.PIXEL_PACK_BUFFER, 0)
			return nil, fmt.Errorf("failed to map PBO for plane %d", i)
		}

		// Copy the data from the mapped PBO into our Go slice
		pixelData := (*[1 << 30]byte)(ptr)[:planeSize:planeSize]
		copy(yuvData[i*planeSize:], pixelData)

		gl.UnmapBuffer(gl.PIXEL_PACK_BUFFER)
	}

	gl.BindBuffer(gl.PIXEL_PACK_BUFFER, 0)
	or.pboIndex = (or.pboIndex + 3) % len(or.pbos)

	return yuvData, nil
}

func findMicChannel(scene *Scene) *inputs.MicChannel {
	if scene == nil {
		return nil
	}
	// It's sufficient to check the named passes as they are a superset
	for _, pass := range scene.NamedPasses {
		for _, ch := range pass.Channels {
			if mic, ok := ch.(*inputs.MicChannel); ok {
				return mic
			}
		}
	}
	return nil
}

func (r *Renderer) RunOffscreen(options *options.ShaderOptions) error {
	if *options.Mode == "stream" {
		return r.runStreamMode(options)
	}
	return r.runRecordMode(options)
}

func (r *Renderer) runStreamMode(options *options.ShaderOptions) error {
	log.Println("Starting in stream mode...")

	ffEncoder, err := encoder.NewFFmpegEncoder(options)
	if err != nil {
		return fmt.Errorf("failed to create CGO encoder: %w", err)
	}
	go ffEncoder.Run()

	hasAudio := r.audioDevice != nil && (*options.AudioInputFile != "" || *options.AudioInputDevice != "")
	if hasAudio {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("Recovered in audio bridge: %v", r)
				}
			}()

			samplesPerFrame := r.audioDevice.SampleRate() / *options.FPS
			ticker := time.NewTicker(time.Second / time.Duration(*options.FPS))
			defer ticker.Stop()

			for range ticker.C {
				samples := r.audioDevice.GetBuffer().Read(samplesPerFrame)
				if len(samples) > 0 {
					ffEncoder.SendAudio(samples)
				}
			}
		}()
	}

	if *options.Prewarm {
		log.Println("Pre-warming renderer...")
		for i := 0; i < len(r.offscreenRenderer.pbos); i++ {
			r.RenderFrame(&inputs.Uniforms{})
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

			r.RenderFrame(uniforms)
			r.RenderToYUV()

			gl.BindFramebuffer(gl.READ_FRAMEBUFFER, r.offscreenRenderer.yuvFbo)
			pixels, err := r.offscreenRenderer.readYUVPixelsAsync(*options.Width, *options.Height)
			gl.BindFramebuffer(gl.READ_FRAMEBUFFER, 0)

			if err != nil {
				log.Printf("Error reading pixels on frame %d: %v", frameCounter, err)
				return ffEncoder.Close()
			}

			// CORRECTED: Use the public SendVideo method
			ffEncoder.SendVideo(&encoder.Frame{Pixels: pixels, PTS: frameCounter})
			frameCounter++
		}
	}
}

func (r *Renderer) runRecordMode(options *options.ShaderOptions) error {
	log.Println("Starting in record mode with CGO encoder...")

	ffEncoder, err := encoder.NewFFmpegEncoder(options)
	if err != nil {
		return fmt.Errorf("failed to create CGO encoder: %w", err)
	}
	go ffEncoder.Run()

	totalFrames := int(*options.Duration * float64(*options.FPS))
	timeStep := 1.0 / float64(*options.FPS)
	sampleRate := r.audioDevice.SampleRate()
	samplesPerFrame := sampleRate / *options.FPS
	micChannel := findMicChannel(r.activeScene)
	hasAudio := r.audioDevice != nil && (*options.AudioInputFile != "" || *options.AudioInputDevice != "" || options.HasSoundShader)

	for i := 0; i < totalFrames; i++ {
		currentTime := float64(i) * timeStep
		uniforms := &inputs.Uniforms{
			Time:      float32(currentTime),
			TimeDelta: float32(timeStep),
			FrameRate: float32(*options.FPS),
			Frame:     int32(i),
		}

		if hasAudio {
			targetSample := int64((currentTime + timeStep) * float64(sampleRate))

			// will block when more audio is needed,
			// and return immediately if the buffer is already sufficient.
			if err := r.audioDevice.DecodeUntil(targetSample); err != nil {
				log.Printf("Error decoding audio: %v. Audio stream will stop.", err)
				ffEncoder.CloseAudio() // Safely close the audio channel
				hasAudio = false       // Prevent further audio processing attempts
			}

			// Read a frame's worth of audio if available.
			if r.audioDevice.GetBuffer().AvailableSamples() > 0 {
				stereoSamples := r.audioDevice.GetBuffer().Read(samplesPerFrame * 2)
				if len(stereoSamples) > 0 {
					ffEncoder.SendAudio(stereoSamples)
				}
			} else {
				log.Println("No audio samples available for this frame, skipping audio send.")
			}

			if micChannel != nil {
				fftStereoChunk := r.audioDevice.GetBuffer().WindowPeek()
				monoSamples := audio.DownmixStereoToMono(fftStereoChunk)
				micChannel.ProcessAudio(monoSamples)
			}
		}

		r.RenderFrame(uniforms)
		r.RenderToYUV()

		gl.BindFramebuffer(gl.READ_FRAMEBUFFER, r.offscreenRenderer.yuvFbo)
		pixels, err := r.offscreenRenderer.readYUVPixelsAsync(*options.Width, *options.Height)
		gl.BindFramebuffer(gl.READ_FRAMEBUFFER, 0)
		if err != nil {
			log.Printf("Error reading pixels on frame %d: %v", i, err)
			break
		}
		ffEncoder.SendVideo(&encoder.Frame{Pixels: pixels, PTS: int64(i)})
	}

	return ffEncoder.Close()
}
