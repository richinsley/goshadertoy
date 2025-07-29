// audio/player.go
package audio

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"time"
	"unsafe"

	options "github.com/richinsley/goshadertoy/options"
)

/*
#cgo pkg-config: libavformat libavcodec libavutil
#include <libavformat/avformat.h>
#include <libavcodec/avcodec.h>
#include <libavutil/opt.h>
#include <libavutil/channel_layout.h>

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
const outputChannelLayout = "mono"
const outputSampleFormat = C.AV_SAMPLE_FMT_FLT // Corresponds to pcm_f32le
const outputFrameSize = 1024                   // A standard audio frame size

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
		format = "pulse"
	case "windows":
		format = "dshow"
	default:
		log.Fatalf("Unsupported OS for live audio playback: %s", runtime.GOOS)
	}
	return
}

// Start begins the audio playback by setting up the FFmpeg pipeline for raw PCM output.
func (p *AudioPlayer) Start(buffer *SharedAudioBuffer) error {
	p.buffer = buffer
	formatName, deviceName := p.getOutputFormatAndDevice()

	// --- Setup Muxer ---
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

	// --- Setup Raw PCM Stream (No Encoder) ---
	p.audioStream = C.avformat_new_stream(p.formatCtx, nil)
	if p.audioStream == nil {
		return fmt.Errorf("could not create new stream")
	}

	p.audioStream.time_base.num = 1
	p.audioStream.time_base.den = outputSampleRate

	codecpar := p.audioStream.codecpar
	codecpar.codec_type = C.AVMEDIA_TYPE_AUDIO
	codecpar.codec_id = C.AV_CODEC_ID_PCM_F32LE
	codecpar.format = outputSampleFormat
	codecpar.sample_rate = outputSampleRate

	cLayoutStr := C.CString(outputChannelLayout)
	defer C.free(unsafe.Pointer(cLayoutStr))
	C.av_channel_layout_from_string(&codecpar.ch_layout, cLayoutStr)

	// --- Open Output and Write Header ---
	// Only call avio_open if the format doesn't handle its own I/O.
	// Device muxers like audiotoolbox have the AVFMT_NOFILE flag set.
	if (outputFormat.flags & C.AVFMT_NOFILE) == 0 {
		if C.avio_open(&p.formatCtx.pb, cDeviceName, C.AVIO_FLAG_WRITE) < 0 {
			return fmt.Errorf("could not open output URL '%s'", deviceName)
		}
	}

	if C.avformat_write_header(p.formatCtx, nil) < 0 {
		return fmt.Errorf("could not write header")
	}

	p.packet = C.av_packet_alloc()

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

	ticker := time.NewTicker(time.Second * outputFrameSize / (outputSampleRate * 2))
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Pull data from the shared buffer
			frameData := p.buffer.ReadLatest(outputFrameSize)
			p.internalBuffer = append(p.internalBuffer, frameData...)
		}

		for len(p.internalBuffer) >= outputFrameSize {
			p.sendFrame(&pts)
		}
	}
}

func (p *AudioPlayer) sendFrame(pts *int64) {
	frameData := p.internalBuffer[:outputFrameSize]
	bufferSize := len(frameData) * 4 // 4 bytes per float32

	if C.av_new_packet(p.packet, C.int(bufferSize)) < 0 {
		log.Println("Error allocating packet")
		return
	}

	cDataPtr := (*[1 << 30]byte)(unsafe.Pointer(p.packet.data))[:bufferSize:bufferSize]
	goSliceAsBytes := (*[1 << 30]byte)(unsafe.Pointer(&frameData[0]))[:bufferSize:bufferSize]
	copy(cDataPtr, goSliceAsBytes)

	p.packet.pts = C.int64_t(*pts)
	p.packet.dts = C.int64_t(*pts)
	p.packet.duration = C.int64_t(len(frameData))
	p.packet.stream_index = p.audioStream.index

	ret := C.av_interleaved_write_frame(p.formatCtx, p.packet)
	if ret < 0 {
		log.Printf("Error writing audio frame: %s", C.GoString(C.av_error_str(ret)))
	}
	C.av_packet_unref(p.packet)

	*pts += int64(len(frameData))
	p.samplesWritten += int64(len(frameData))
	p.internalBuffer = p.internalBuffer[outputFrameSize:]
}

// cleanup writes the trailer and frees all resources.
func (p *AudioPlayer) cleanup() {
	if p.formatCtx != nil {
		C.av_write_trailer(p.formatCtx)
	}

	if p.packet != nil {
		C.av_packet_free(&p.packet)
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

// Stop terminates the audio playback.
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
