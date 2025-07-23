package audio

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"

	options "github.com/richinsley/goshadertoy/options"
	semaphore "github.com/richinsley/goshadertoy/semaphore"
	sharedmemory "github.com/richinsley/goshadertoy/sharedmemory"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

/*
#cgo CFLAGS: -I../shmframe
#include "protocol.h"
*/
import "C"

// FFmpegAudioDevice implements the AudioDevice interface using FFmpeg.
type FFmpegAudioDevice struct {
	inputFile   string
	ffmpegPath  string
	cmd         *exec.Cmd
	pipeReader  io.ReadCloser
	audioChan   chan []float32
	stopChan    chan struct{}
	shm         *sharedmemory.SharedMemory
	emptySem    semaphore.Semaphore
	fullSem     semaphore.Semaphore
	sampleRate  int
	isStreaming bool
	options     *options.ShaderOptions
	player      *AudioPlayer
}

// NewFFmpegAudioDevice creates a new audio device that sources its audio from an FFmpeg process.
func NewFFmpegAudioDevice(options *options.ShaderOptions) (*FFmpegAudioDevice, error) {
	retv := &FFmpegAudioDevice{
		ffmpegPath: *options.FFMPEGPath,
		stopChan:   make(chan struct{}),
		options:    options,
	}
	if *options.AudioInputDevice != "" {
		retv.inputFile = *options.AudioInputDevice
	} else if *options.AudioInputFile != "" {
		retv.inputFile = *options.AudioInputFile
	} else {
		log.Println("No audio input device or file specified.")
		return nil, fmt.Errorf("no audio input device or file specified")
	}

	// If an output device is specified, create a player instance.
	if *options.AudioOutputDevice != "" {
		player, err := NewAudioPlayer(options)
		if err != nil {
			return nil, err
		}
		retv.player = player
	}

	return retv, nil
}

func (d *FFmpegAudioDevice) Start() (<-chan []float32, error) {
	d.audioChan = make(chan []float32, 16)

	pipeReader, pipeWriter := io.Pipe()
	d.pipeReader = pipeReader

	log.Printf("Starting FFmpeg audio capture for input: %s", d.inputFile)

	inputArgs := ffmpeg.KwArgs{}
	if *d.options.AudioInputDevice != "" {
		inputArgs["f"] = "avfoundation" // Default for macOS
		inputArgs["fflags"] = "nobuffer"
	} else if *d.options.AudioInputFile != "" {
		if *d.options.Mode == "stream" || *d.options.Mode == "live" {
			inputArgs["re"] = ""
		}
	}

	outputArgs := ffmpeg.KwArgs{
		"f":                  "shm_muxer",
		"samples_per_buffer": "1024", // This must match the logic in the client
		"c:a":                "pcm_f32le",
		"ar":                 "44100",
		"ac":                 "1",
		"flush_packets":      "1",
	}

	ffmpegNode := ffmpeg.Input(d.inputFile, inputArgs)
	ffmpegCmd := ffmpegNode.Output("pipe:", outputArgs).
		WithOutput(pipeWriter).ErrorToStdOut()

	if d.ffmpegPath != "" {
		ffmpegCmd.SetFfmpegPath(d.ffmpegPath)
	}

	d.cmd = ffmpegCmd.Compile()
	d.cmd.Stderr = os.Stderr

	go func() {
		err := d.cmd.Run()
		if err != nil && !strings.Contains(err.Error(), "signal: killed") {
			log.Printf("FFmpeg command finished with error: %v", err)
		}
		pipeWriter.Close()
	}()

	if d.player != nil {
		playerChan := make(chan []float32, 16)
		d.player.Start(playerChan)
		producerChan := make(chan []float32, 16)
		go Tee(producerChan, d.audioChan, playerChan)
		go func() {
			defer close(producerChan)
			d.runAudioLoop(producerChan)
		}()
	} else {
		go func() {
			defer close(d.audioChan)
			d.runAudioLoop(d.audioChan)
		}()
	}

	d.isStreaming = true
	return d.audioChan, nil
}

// runAudioLoop now accepts one or more channels to write to.
func (d *FFmpegAudioDevice) runAudioLoop(outputs ...chan<- []float32) {

	defer func() {
		if d.shm != nil {
			d.shm.Close()
		}
		if d.emptySem != nil {
			d.emptySem.Close()
		}
		if d.fullSem != nil {
			d.fullSem.Close()
		}
		log.Println("FFmpeg audio loop finished.")
	}()

	var shmHeader C.SHMHeader
	headerSize := int(unsafe.Sizeof(shmHeader))
	headerBuf := make([]byte, headerSize)

	if _, err := io.ReadFull(d.pipeReader, headerBuf); err != nil {
		log.Printf("Failed to read SHMHeader from FFmpeg pipe: %v", err)
		return
	}

	shmHeader = *(*C.SHMHeader)(unsafe.Pointer(&headerBuf[0]))
	d.sampleRate = int(shmHeader.sample_rate)
	log.Printf("Received SHMHeader from FFmpeg: SampleRate=%d, Channels=%d", d.sampleRate, shmHeader.channels)

	shmNameFromHeader := C.GoString(&shmHeader.shm_file_audio[0])
	shmNameForGo := strings.TrimPrefix(shmNameFromHeader, "/")
	emptySemName := C.GoString(&shmHeader.empty_sem_name_audio[0])
	fullSemName := C.GoString(&shmHeader.full_sem_name_audio[0])

	var err error
	d.emptySem, err = semaphore.OpenSemaphore(emptySemName)
	if err != nil {
		log.Printf("Failed to open empty semaphore '%s': %v", emptySemName, err)
		return
	}
	d.fullSem, err = semaphore.OpenSemaphore(fullSemName)
	if err != nil {
		log.Printf("Failed to open full semaphore '%s': %v", fullSemName, err)
		return
	}

	const samplesPerBuffer = 1024
	const numBuffers = 3
	bytesPerSample := int(shmHeader.bit_depth / 8)
	frameSize := samplesPerBuffer * int(shmHeader.channels) * bytesPerSample
	shmSize := int(unsafe.Sizeof(C.SHMControlBlock{})) + (frameSize * numBuffers)

	d.shm, err = sharedmemory.OpenSharedMemory(shmNameForGo, shmSize)
	if err != nil {
		log.Printf("Failed to open shared memory '%s': %v", shmNameForGo, err)
		return
	}

	var frameHeader C.FrameHeader
	frameHeaderSize := int(unsafe.Sizeof(frameHeader))
	frameHeaderBuf := make([]byte, frameHeaderSize)

	for {
		select {
		case <-d.stopChan:
			return
		default:
			_, err := io.ReadFull(d.pipeReader, frameHeaderBuf)
			if err != nil {
				return
			}

			frameHeader = *(*C.FrameHeader)(unsafe.Pointer(&frameHeaderBuf[0]))

			if frameHeader.cmdtype == 2 { // EOF
				return
			}

			if frameHeader.cmdtype == 1 { // Audio Data
				if err := d.fullSem.Acquire(); err != nil {
					return
				}

				audioData := make([]byte, frameHeader.size)
				n, err := d.shm.ReadAt(audioData, int64(frameHeader.offset))
				if err != nil || n != int(frameHeader.size) {
					// handle error
				} else {
					floatData := make([]float32, len(audioData)/4)
					reader := bytes.NewReader(audioData)
					binary.Read(reader, binary.LittleEndian, &floatData)

					for _, out := range outputs {
						out <- floatData
					}
				}

				if err := d.emptySem.Release(); err != nil {
					return
				}
			}
		}
	}
}

func (d *FFmpegAudioDevice) Stop() error {
	if !d.isStreaming {
		return nil
	}
	d.isStreaming = false
	close(d.stopChan)

	if d.player != nil {
		d.player.Stop()
	}

	if d.cmd != nil && d.cmd.Process != nil {
		if err := d.cmd.Process.Signal(syscall.SIGINT); err != nil {
			log.Printf("Failed to send SIGINT to FFmpeg, killing process: %v", err)
			d.cmd.Process.Kill()
		}
	}
	return nil
}

func (d *FFmpegAudioDevice) SampleRate() int {
	return d.sampleRate
}
