package audio

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log"
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
	return retv, nil
}

func (d *FFmpegAudioDevice) getArgs() (inputdev string, inputArgs ffmpeg.KwArgs, outputArgs ffmpeg.KwArgs) {
	inputArgs = ffmpeg.KwArgs{}
	if *d.options.AudioInputDevice != "" {
		inputdev = *d.options.AudioInputDevice
		inputArgs["f"] = "avfoundation" // Default for macOS
		// This flag can help reduce latency
		inputArgs["fflags"] = "nobuffer"

	} else if *d.options.AudioInputFile != "" {
		inputdev = *d.options.AudioInputFile
		// rate emulation for stream and live modes
		if *d.options.Mode == "stream" || *d.options.Mode == "live" {
			inputArgs["re"] = "" // Read input in real-time for streaming
		}
	}

	outputArgs = ffmpeg.KwArgs{
		"f":             "shm_muxer",
		"c:a":           "pcm_f32le",
		"ar":            "44100", // Set a consistent sample rate
		"ac":            "1",     // Set a consistent channel layout
		"flush_packets": "1",     // Force flush packets to the output pipe
	}

	return inputdev, inputArgs, outputArgs
}

func (d *FFmpegAudioDevice) Start() (<-chan []float32, error) {
	inputdev, inputArgs, outputArgs := d.getArgs()
	d.audioChan = make(chan []float32, 16) // Buffered channel

	pipeReader, pipeWriter := io.Pipe()
	d.pipeReader = pipeReader

	log.Printf("Starting FFmpeg audio capture for input: %s", d.inputFile)

	// Build the FFmpeg command chain
	ffmpegNode := ffmpeg.Input(inputdev, inputArgs)

	// force ffmpeg to process audio in 1024-sample chunks.
	ffmpegNode = ffmpegNode.Filter("asetnsamples", ffmpeg.Args{"1024"})

	ffmpegCmd := ffmpegNode.Output("pipe:", outputArgs).
		WithOutput(pipeWriter).ErrorToStdOut()

	if d.ffmpegPath != "" {
		ffmpegCmd.SetFfmpegPath(d.ffmpegPath)
	}

	d.cmd = ffmpegCmd.Compile()

	// Now, run the command object we just created and stored.
	go func() {
		err := d.cmd.Run()
		if err != nil && !strings.Contains(err.Error(), "signal: killed") {

			log.Printf("FFmpeg command finished with error: %v", err)
		}
		// When FFmpeg is done (or fails), close the writer. This will unblock
		// the reader in the main goroutine and signal that there's no more data.
		pipeWriter.Close()
	}()

	go d.runAudioLoop()

	d.isStreaming = true
	return d.audioChan, nil
}

func (d *FFmpegAudioDevice) runAudioLoop() {
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
		close(d.audioChan)
		log.Println("FFmpeg audio loop finished.")
	}()

	var shmHeader C.SHMHeader
	headerSize := int(unsafe.Sizeof(shmHeader))
	headerBuf := make([]byte, headerSize)

	// Read the SHMHeader from FFmpeg's output pipe
	if _, err := io.ReadFull(d.pipeReader, headerBuf); err != nil {
		log.Printf("Failed to read SHMHeader from FFmpeg pipe: %v", err)
		return
	}

	// Directly cast the byte buffer to the C struct pointer
	shmHeader = *(*C.SHMHeader)(unsafe.Pointer(&headerBuf[0]))

	d.sampleRate = int(shmHeader.sample_rate)
	log.Printf("Received SHMHeader from FFmpeg: SampleRate=%d, Channels=%d", d.sampleRate, shmHeader.channels)

	shmNameFromHeader := C.GoString(&shmHeader.shm_file[0])
	shmNameForGo := strings.TrimPrefix(shmNameFromHeader, "/")
	emptySemName := C.GoString(&shmHeader.empty_sem_name[0])
	fullSemName := C.GoString(&shmHeader.full_sem_name[0])

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

	// Calculate SHM size based on producer's logic
	frameSize := int(shmHeader.sample_rate*shmHeader.channels*(shmHeader.bit_depth/8)) * 2
	if frameSize < 4096 {
		frameSize = 4096
	}
	// numBuffers is 3 in the C code
	shmSize := int(unsafe.Sizeof(C.SHMControlBlock{})) + (frameSize * 3)

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
			log.Println("Stop signal received, exiting audio loop.")
			return
		default:
			_, err := io.ReadFull(d.pipeReader, frameHeaderBuf)
			if err != nil {
				if err == io.EOF || err == io.ErrClosedPipe {
					log.Println("FFmpeg pipe closed. Stopping audio loop gracefully.")
				} else {
					log.Printf("Error reading FrameHeader from pipe: %v", err)
				}
				return
			}

			frameHeader = *(*C.FrameHeader)(unsafe.Pointer(&frameHeaderBuf[0]))

			if frameHeader.cmdtype == 2 { // EOF
				log.Println("Received EOF command from FFmpeg.")
				return
			}

			if frameHeader.cmdtype == 0 { // Audio Data
				if err := d.fullSem.Acquire(); err != nil {
					log.Printf("Error acquiring full semaphore: %v", err)
					return
				}

				audioData := make([]byte, frameHeader.size)
				n, err := d.shm.ReadAt(audioData, int64(frameHeader.offset))
				if err != nil || n != int(frameHeader.size) {
					log.Printf("Error reading audio data from shared memory: %v", err)
				} else {
					floatData := make([]float32, len(audioData)/4)
					reader := bytes.NewReader(audioData)
					binary.Read(reader, binary.LittleEndian, &floatData)
					d.audioChan <- floatData
				}

				if err := d.emptySem.Release(); err != nil {
					log.Printf("Error releasing empty semaphore: %v", err)
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

	if d.cmd != nil && d.cmd.Process != nil {
		// Send SIGINT to FFmpeg for a graceful shutdown
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
