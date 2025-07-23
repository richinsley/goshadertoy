package audio

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"unsafe"

	options "github.com/richinsley/goshadertoy/options"
	semaphore "github.com/richinsley/goshadertoy/semaphore"
	sharedmemory "github.com/richinsley/goshadertoy/sharedmemory"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

/*
#cgo CFLAGS: -I../shmframe
#include <string.h>
#include "protocol.h"
*/
import "C"

const numAudioBuffers = 3

// AudioPlayer plays audio data using FFmpeg via shared memory.
type AudioPlayer struct {
	cmd         *exec.Cmd
	pipeWriter  io.WriteCloser
	options     *options.ShaderOptions
	shm         *sharedmemory.SharedMemory
	emptySem    semaphore.Semaphore
	fullSem     semaphore.Semaphore
	stopChan    chan struct{}
	isStreaming bool

	internalBuffer          []byte
	internalBufferOccupancy int
	frameBufferSize         int
}

// NewAudioPlayer creates a new audio player.
func NewAudioPlayer(options *options.ShaderOptions) (*AudioPlayer, error) {
	if *options.AudioOutputDevice == "" {
		return nil, fmt.Errorf("no audio output device specified")
	}

	p := &AudioPlayer{
		options:  options,
		stopChan: make(chan struct{}),
	}

	return p, nil
}

// getOutputArgs configures the FFmpeg output arguments based on the OS.
func (p *AudioPlayer) getOutputArgs() (string, ffmpeg.KwArgs) {
	outputArgs := ffmpeg.KwArgs{}
	device := *p.options.AudioOutputDevice

	switch runtime.GOOS {
	case "darwin":
		outputArgs["f"] = "audiotoolbox"
		// audiotoolbox requires a destination.
		device = "/dev/null"
	case "linux":
		outputArgs["f"] = "pulse"
	case "windows":
		outputArgs["f"] = "dshow"
	default:
		log.Fatalf("Unsupported OS for live audio playback: %s", runtime.GOOS)
	}
	return device, outputArgs
}

// Start begins the audio playback.
func (p *AudioPlayer) Start(input <-chan []float32) error {
	outputDevice, outputArgs := p.getOutputArgs()

	pid := os.Getpid()
	shmNameStr := fmt.Sprintf("goshadertoy_player_%d", pid)
	emptySemName := fmt.Sprintf("goshadertoy_player_empty_%d", pid)
	fullSemName := fmt.Sprintf("goshadertoy_player_full_%d", pid)

	semaphore.RemoveSemaphore(emptySemName)
	semaphore.RemoveSemaphore(fullSemName)

	p.frameBufferSize = 4096 // 1024 samples * 4 bytes/sample
	shmSize := int(unsafe.Sizeof(C.SHMControlBlock{})) + (p.frameBufferSize * numAudioBuffers)

	p.internalBuffer = make([]byte, p.frameBufferSize*2)
	p.internalBufferOccupancy = 0

	var err error
	p.shm, err = sharedmemory.CreateSharedMemory(shmNameStr, shmSize)
	if err != nil {
		return fmt.Errorf("player: failed to create shared memory: %w", err)
	}

	p.emptySem, err = semaphore.NewSemaphore(emptySemName, numAudioBuffers)
	if err != nil {
		p.shm.Close()
		return fmt.Errorf("player: failed to create empty semaphore: %w", err)
	}

	p.fullSem, err = semaphore.NewSemaphore(fullSemName, 0)
	if err != nil {
		p.shm.Close()
		p.emptySem.Close()
		semaphore.RemoveSemaphore(emptySemName)
		return fmt.Errorf("player: failed to create full semaphore: %w", err)
	}

	pipeReader, pipeWriter := io.Pipe()
	p.pipeWriter = pipeWriter

	inputArgs := ffmpeg.KwArgs{
		"f":  "shm_demuxer",
		"re": "",
	}

	ffmpegCmdNode := ffmpeg.Input("pipe:", inputArgs).
		Output(outputDevice, outputArgs).
		WithInput(pipeReader)

	if *p.options.FFMPEGPath != "" {
		ffmpegCmdNode = ffmpegCmdNode.SetFfmpegPath(*p.options.FFMPEGPath)
	}

	p.cmd = ffmpegCmdNode.Compile()

	p.cmd.Stdout = nil
	p.cmd.Stderr = log.Writer()

	go func() {
		err := p.cmd.Run()
		if err != nil && !strings.Contains(err.Error(), "signal: killed") {
			log.Printf("FFmpeg audio player command finished with error: %v", err)
		}
		pipeWriter.Close()
	}()

	header := C.SHMHeader{
		stream_count: 1, sample_rate: 44100, channels: 1, bit_depth: 32, version: 1,
	}
	C.strncpy((*C.char)(unsafe.Pointer(&header.shm_file_audio[0])), C.CString("/"+shmNameStr), 511)
	C.strncpy((*C.char)(unsafe.Pointer(&header.empty_sem_name_audio[0])), C.CString(emptySemName), 255)
	C.strncpy((*C.char)(unsafe.Pointer(&header.full_sem_name_audio[0])), C.CString(fullSemName), 255)

	headerBytes := (*[unsafe.Sizeof(header)]byte)(unsafe.Pointer(&header))[:]
	if _, err := pipeWriter.Write(headerBytes); err != nil {
		return fmt.Errorf("failed to write header to FFmpeg player: %w", err)
	}

	go p.runProducer(input)
	p.isStreaming = true

	return nil
}

// writeFullFrameToSHM writes one complete, fixed-size frame to shared memory.
func (p *AudioPlayer) writeFullFrameToSHM(writeIndex *uint32, pts *int64) error {
	if err := p.emptySem.Acquire(); err != nil {
		return fmt.Errorf("producer failed to acquire empty semaphore: %w", err)
	}

	writeOffset := int64(unsafe.Sizeof(C.SHMControlBlock{})) + (int64(*writeIndex) * int64(p.frameBufferSize))

	// Use the buffered data directly
	_, err := p.shm.WriteAt(p.internalBuffer[:p.frameBufferSize], writeOffset)
	if err != nil {
		p.emptySem.Release()
		return fmt.Errorf("producer failed to write to shared memory: %w", err)
	}

	frameHeader := C.FrameHeader{
		cmdtype: 1, // audio
		size:    C.uint32_t(p.frameBufferSize),
		pts:     C.int64_t(*pts),
		offset:  C.uint64_t(writeOffset),
	}
	// A full frame corresponds to frameBufferSize / 4 samples
	*pts += int64(p.frameBufferSize / 4)

	frameHeaderBytes := (*[unsafe.Sizeof(frameHeader)]byte)(unsafe.Pointer(&frameHeader))[:]
	if _, err := p.pipeWriter.Write(frameHeaderBytes); err != nil {
		p.emptySem.Release()
		return fmt.Errorf("producer failed to write frame header: %w", err)
	}

	*writeIndex = (*writeIndex + 1) % numAudioBuffers

	p.internalBufferOccupancy -= p.frameBufferSize
	if p.internalBufferOccupancy > 0 {
		copy(p.internalBuffer, p.internalBuffer[p.frameBufferSize:p.frameBufferSize+p.internalBufferOccupancy])
	}

	if err := p.fullSem.Release(); err != nil {
		log.Printf("Producer failed to release full semaphore: %v", err)
	}

	return nil
}

// runProducer is the loop that takes audio data, buffers it, and writes full frames to shared memory.
func (p *AudioPlayer) runProducer(input <-chan []float32) {
	defer func() {
		if p.pipeWriter != nil {
			controlBlockPtr := (*C.SHMControlBlock)(p.shm.GetPtr())
			controlBlockPtr.eof = 1
			eofHeader := C.FrameHeader{cmdtype: C.uint32_t(2)}
			eofHeaderBytes := (*[unsafe.Sizeof(eofHeader)]byte)(unsafe.Pointer(&eofHeader))[:]
			p.pipeWriter.Write(eofHeaderBytes)
			p.pipeWriter.Close()
		}
	}()

	controlBlockPtr := (*C.SHMControlBlock)(p.shm.GetPtr())
	controlBlockPtr.num_buffers = numAudioBuffers
	controlBlockPtr.eof = 0

	var writeIndex uint32 = 0
	var pts int64 = 0

	for {
		select {
		case <-p.stopChan:
			return
		case data, ok := <-input:
			if !ok {
				if p.internalBufferOccupancy > 0 {
					paddingSize := p.frameBufferSize - p.internalBufferOccupancy
					if paddingSize > 0 {
						padding := make([]byte, paddingSize)
						copy(p.internalBuffer[p.internalBufferOccupancy:], padding)
						p.internalBufferOccupancy += paddingSize
					}
					p.writeFullFrameToSHM(&writeIndex, &pts)
				}
				return
			}

			// Efficiently convert []float32 to []byte without extra allocations
			if len(data) > 0 {
				byteData := (*[1 << 30]byte)(unsafe.Pointer(&data[0]))[: len(data)*4 : len(data)*4]
				copy(p.internalBuffer[p.internalBufferOccupancy:], byteData)
				p.internalBufferOccupancy += len(byteData)
			}

			for p.internalBufferOccupancy >= p.frameBufferSize {
				if err := p.writeFullFrameToSHM(&writeIndex, &pts); err != nil {
					log.Printf("Error writing frame to SHM: %v", err)
					return
				}
			}
		}
	}
}

// Stop terminates the audio playback.
func (p *AudioPlayer) Stop() error {
	if !p.isStreaming {
		return nil
	}
	p.isStreaming = false
	close(p.stopChan)

	if p.shm != nil {
		p.shm.Close()
	}
	if p.emptySem != nil {
		p.emptySem.Close()
		semaphore.RemoveSemaphore(fmt.Sprintf("goshadertoy_player_empty_%d", os.Getpid()))
	}
	if p.fullSem != nil {
		p.fullSem.Close()
		semaphore.RemoveSemaphore(fmt.Sprintf("goshadertoy_player_full_%d", os.Getpid()))
	}

	if p.cmd != nil && p.cmd.Process != nil {
		return p.cmd.Process.Kill()
	}
	return nil
}
