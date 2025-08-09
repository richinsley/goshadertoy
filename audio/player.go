// audio/player.go
package audio

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"time"
	"unsafe"

	"github.com/richinsley/goshadertoy/arcana"
	options "github.com/richinsley/goshadertoy/options"
)

/*
#cgo CFLAGS: -I${SRCDIR}/../release/include -I${SRCDIR}/../release/include/arcana
#include <libavformat/avformat.h>
#include <libavcodec/avcodec.h>
#include <libavutil/opt.h>
#include <libavutil/channel_layout.h>
#include <libavutil/samplefmt.h>
#include <libswresample/swresample.h>

// C wrapper to find an output format by its short name
static const AVOutputFormat* find_output_format(const char* name) {
    const AVOutputFormat *fmt = NULL;
    void *opaque = NULL;
    while ((fmt = av_muxer_iterate(&opaque))) {
        if (strcmp(fmt->name, name) == 0) {
            return fmt;
        }
    }
    return NULL;
}

// av_err2str is a macro, so we need a wrapper function
static inline const char* av_error_str(int errnum) {
    static char str[AV_ERROR_MAX_STRING_SIZE];
    av_make_error_string(str, AV_ERROR_MAX_STRING_SIZE, errnum);
    return str;
}
*/
import "C"

const outputSampleRate = 44100
const outputChannelLayout = "stereo"
const outputChannels = 2
const outputFrameSize = 1024 // A standard audio frame size

// AudioPlayer plays raw audio data using FFmpeg device muxers.
type AudioPlayer struct {
	formatCtx      *C.AVFormatContext
	audioStream    *C.AVStream
	packet         *C.AVPacket
	isStreaming    bool
	options        *options.ShaderOptions
	internalBuffer []float32
	startTime      time.Time
	samplesWritten int64
	buffer         *SharedAudioBuffer
	cancel         context.CancelFunc

	// --- Re-instated frames for robust memory management ---
	swrCtx             *C.struct_SwrContext
	srcFrame           *C.AVFrame // Reusable source frame (always FLT)
	dstFrame           *C.AVFrame // Reusable destination frame (target format)
	targetSampleFormat C.enum_AVSampleFormat
	targetCodecID      C.enum_AVCodecID
}

// NewAudioPlayer creates a new audio player.
func NewAudioPlayer(options *options.ShaderOptions) (*AudioPlayer, error) {
	if *options.AudioOutputDevice == "" {
		return nil, fmt.Errorf("no audio output device specified")
	}

	p := &AudioPlayer{
		options:        options,
		internalBuffer: make([]float32, 0, outputFrameSize*4), // Pre-allocate some capacity
	}

	return p, nil
}

// getOutputFormatAndDevice determines the correct FFmpeg format and device string based on the OS.
func (p *AudioPlayer) getOutputFormatAndDevice() (format, device string) {
	device = *p.options.AudioOutputDevice
	switch runtime.GOOS {
	case "darwin":
		format = "audiotoolbox"
	case "linux":
		format = "alsa"
	case "windows":
		format = "dshow"
	default:
		log.Fatalf("Unsupported OS for live audio playback: %s", runtime.GOOS)
	}
	return format, device
}

// Start begins the audio playback by setting up the FFmpeg pipeline for raw PCM output.
func (p *AudioPlayer) Start(buffer *SharedAudioBuffer) error {
	p.buffer = buffer
	formatName, deviceName := p.getOutputFormatAndDevice()

	var err error
	p.targetSampleFormat, err = arcana.ProbeDeviceForBestFormat(deviceName, outputChannels, outputSampleRate)
	if err != nil {
		log.Printf("Device probe failed: %v. Falling back to S16_LE.", err)
		p.targetSampleFormat = C.AV_SAMPLE_FMT_S16
	}

	switch p.targetSampleFormat {
	case C.AV_SAMPLE_FMT_FLT:
		p.targetCodecID = C.AV_CODEC_ID_PCM_F32LE
	case C.AV_SAMPLE_FMT_S32:
		p.targetCodecID = C.AV_CODEC_ID_PCM_S32LE
	case C.AV_SAMPLE_FMT_S16:
		p.targetCodecID = C.AV_CODEC_ID_PCM_S16LE
	default:
		log.Printf("Warning: Unknown target format, defaulting to S16_LE")
		p.targetSampleFormat = C.AV_SAMPLE_FMT_S16
		p.targetCodecID = C.AV_CODEC_ID_PCM_S16LE
	}

	cFormatName := C.CString(formatName)
	defer C.free(unsafe.Pointer(cFormatName))
	cDeviceName := C.CString(deviceName)
	defer C.free(unsafe.Pointer(cDeviceName))
	outputFormat := C.find_output_format(cFormatName)
	if outputFormat == nil {
		return fmt.Errorf("could not find output format '%s'", formatName)
	}
	if C.avformat_alloc_output_context2(&p.formatCtx, outputFormat, nil, cDeviceName) < 0 {
		return fmt.Errorf("could not create output context")
	}

	p.audioStream = C.avformat_new_stream(p.formatCtx, nil)
	if p.audioStream == nil {
		p.cleanup()
		return fmt.Errorf("could not create new stream")
	}
	p.audioStream.time_base.num = 1
	p.audioStream.time_base.den = outputSampleRate
	codecpar := p.audioStream.codecpar
	codecpar.codec_type = C.AVMEDIA_TYPE_AUDIO
	codecpar.codec_id = p.targetCodecID
	codecpar.format = C.int(p.targetSampleFormat)
	codecpar.sample_rate = outputSampleRate
	cLayoutStr := C.CString(outputChannelLayout)
	defer C.free(unsafe.Pointer(cLayoutStr))
	C.av_channel_layout_from_string(&codecpar.ch_layout, cLayoutStr)

	var outChLayout, inChLayout C.AVChannelLayout
	C.av_channel_layout_from_string(&outChLayout, cLayoutStr)
	C.av_channel_layout_from_string(&inChLayout, cLayoutStr)
	defer C.av_channel_layout_uninit(&outChLayout)
	defer C.av_channel_layout_uninit(&inChLayout)

	C.swr_alloc_set_opts2(&p.swrCtx, &outChLayout, p.targetSampleFormat, C.int(outputSampleRate), &inChLayout, C.AV_SAMPLE_FMT_FLT, C.int(outputSampleRate), 0, nil)
	if p.swrCtx == nil {
		p.cleanup()
		return fmt.Errorf("could not allocate resampler context")
	}
	if C.swr_init(p.swrCtx) < 0 {
		p.cleanup()
		return fmt.Errorf("failed to initialize resampler context")
	}

	// --- Allocate and configure reusable AVFrames ---
	p.srcFrame = C.av_frame_alloc()
	p.dstFrame = C.av_frame_alloc()
	p.packet = C.av_packet_alloc()
	if p.srcFrame == nil || p.dstFrame == nil || p.packet == nil {
		p.cleanup()
		return fmt.Errorf("could not allocate frame or packet")
	}

	p.srcFrame.format = C.AV_SAMPLE_FMT_FLT
	p.srcFrame.nb_samples = C.int(outputFrameSize)
	C.av_channel_layout_copy(&p.srcFrame.ch_layout, &inChLayout)
	if C.av_frame_get_buffer(p.srcFrame, 0) < 0 {
		p.cleanup()
		return fmt.Errorf("could not allocate src frame buffer")
	}

	p.dstFrame.format = C.int(p.targetSampleFormat)
	p.dstFrame.nb_samples = C.int(outputFrameSize)
	C.av_channel_layout_copy(&p.dstFrame.ch_layout, &outChLayout)
	if C.av_frame_get_buffer(p.dstFrame, 0) < 0 {
		p.cleanup()
		return fmt.Errorf("could not allocate dst frame buffer")
	}

	if (outputFormat.flags & C.AVFMT_NOFILE) == 0 {
		if C.avio_open(&p.formatCtx.pb, cDeviceName, C.AVIO_FLAG_WRITE) < 0 {
			p.cleanup()
			return fmt.Errorf("could not open output URL '%s'", deviceName)
		}
	}
	if C.avformat_write_header(p.formatCtx, nil) < 0 {
		p.cleanup()
		return fmt.Errorf("could not write header")
	}

	var ctx context.Context
	ctx, p.cancel = context.WithCancel(context.Background())
	go p.runOutputLoop(ctx)
	p.isStreaming = true

	return nil
}

// runOutputLoop implements a buffering and pacing strategy to send fixed-size audio chunks.
func (p *AudioPlayer) runOutputLoop(ctx context.Context) {
	defer p.cleanup()
	var pts int64 = 0
	p.startTime = time.Now()
	p.samplesWritten = 0

	ticker := time.NewTicker(time.Second * outputFrameSize / (outputSampleRate))
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			frameData := p.buffer.Read(outputFrameSize * outputChannels)
			p.internalBuffer = append(p.internalBuffer, frameData...)
		}
		for len(p.internalBuffer) >= outputFrameSize*outputChannels {
			p.sendFrame(&pts)
		}
	}
}

func (p *AudioPlayer) sendFrame(pts *int64) {
	// Get a chunk of float32 audio from our internal buffer.
	frameSamples := p.internalBuffer[:outputFrameSize*outputChannels]
	// inputSampleCount := C.int(outputFrameSize)

	// Make sure the source frame is writable and copy our Go data into it.
	if C.av_frame_make_writable(p.srcFrame) < 0 {
		log.Println("Source frame not writable")
		return
	}
	srcDataPtr := unsafe.Pointer(p.srcFrame.data[0])
	srcSlice := (*[1 << 30]byte)(srcDataPtr)[:len(frameSamples)*4]
	goSliceBytes := (*[1 << 30]byte)(unsafe.Pointer(&frameSamples[0]))[:len(frameSamples)*4]
	copy(srcSlice, goSliceBytes)

	// Use swr_convert, passing pointers to the pre-allocated frame data buffers.
	convertedSamples := C.swr_convert(p.swrCtx, &p.dstFrame.data[0], p.dstFrame.nb_samples, &p.srcFrame.data[0], p.srcFrame.nb_samples)
	if convertedSamples < 0 {
		log.Println("Error during swr_convert")
		return
	}

	// Create a packet directly from the data in the *destination* frame.
	bufferSize := C.av_samples_get_buffer_size(nil, p.dstFrame.ch_layout.nb_channels, convertedSamples, p.targetSampleFormat, 1)
	if C.av_new_packet(p.packet, bufferSize) < 0 {
		log.Println("Error allocating packet")
		return
	}
	copy((*[1 << 30]byte)(unsafe.Pointer(p.packet.data))[:bufferSize], (*[1 << 30]byte)(unsafe.Pointer(p.dstFrame.data[0]))[:bufferSize])

	p.packet.pts = C.int64_t(*pts)
	p.packet.dts = p.packet.pts
	p.packet.duration = C.int64_t(convertedSamples)
	p.packet.stream_index = p.audioStream.index

	// Write the packet.
	if C.av_interleaved_write_frame(p.formatCtx, p.packet) < 0 {
		log.Printf("Error writing audio frame")
	}
	C.av_packet_unref(p.packet)

	// Update counters.
	p.samplesWritten += int64(convertedSamples)
	*pts = p.samplesWritten
	p.internalBuffer = p.internalBuffer[outputFrameSize*outputChannels:]
}

func (p *AudioPlayer) cleanup() {
	if p.formatCtx != nil {
		C.av_write_trailer(p.formatCtx)
	}
	if p.packet != nil {
		C.av_packet_free(&p.packet)
	}
	// Free the frames that were allocated in Start()
	if p.srcFrame != nil {
		C.av_frame_free(&p.srcFrame)
	}
	if p.dstFrame != nil {
		C.av_frame_free(&p.dstFrame)
	}
	if p.swrCtx != nil {
		C.swr_free(&p.swrCtx)
	}
	if p.formatCtx != nil {
		if p.audioStream != nil && p.audioStream.codecpar != nil {
			C.av_channel_layout_uninit(&p.audioStream.codecpar.ch_layout)
		}
		if p.formatCtx.pb != nil {
			C.avio_closep(&p.formatCtx.pb)
		}
		C.avformat_free_context(p.formatCtx)
	}
	log.Println("Audio player resources cleaned up.")
}

func (p *AudioPlayer) Stop() error {
	if !p.isStreaming {
		return nil
	}
	p.isStreaming = false
	if p.cancel != nil {
		p.cancel()
	}
	return nil
}
