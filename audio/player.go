// package audio

// import (
// 	"bytes"
// 	"encoding/binary"
// 	"fmt"
// 	"io"
// 	"log"
// 	"os"
// 	"os/exec"
// 	"runtime"
// 	"unsafe"

// 	"github.com/richinsley/goshadertoy/options"
// 	"github.com/richinsley/goshadertoy/semaphore"
// 	"github.com/richinsley/goshadertoy/sharedmemory"
// 	ffmpeg "github.com/u2takey/ffmpeg-go"
// )

// /*
// #cgo CFLAGS: -I../shmframe
// #include <string.h>
// #include "protocol.h"
// */
// import "C"

// const numAudioBuffers = 3

// // AudioPlayer plays audio data using FFmpeg via shared memory.
// type AudioPlayer struct {
// 	cmd         *exec.Cmd
// 	pipeWriter  io.WriteCloser
// 	options     *options.ShaderOptions
// 	shm         *sharedmemory.SharedMemory
// 	emptySem    semaphore.Semaphore
// 	fullSem     semaphore.Semaphore
// 	stopChan    chan struct{}
// 	isStreaming bool
// }

// // NewAudioPlayer creates a new audio player.
// func NewAudioPlayer(options *options.ShaderOptions) (*AudioPlayer, error) {
// 	if *options.AudioOutputDevice == "" {
// 		return nil, fmt.Errorf("no audio output device specified")
// 	}

// 	p := &AudioPlayer{
// 		options:  options,
// 		stopChan: make(chan struct{}),
// 	}

// 	return p, nil
// }

// func (p *AudioPlayer) getArgs() (outputDevice string, outputArgs ffmpeg.KwArgs) {
// 	outputArgs = ffmpeg.KwArgs{}
// 	switch runtime.GOOS {
// 	case "darwin":
// 		outputArgs["f"] = "audiotoolbox"
// 		outputArgs["audio_device_index"] = *p.options.AudioOutputDevice
// 		*p.options.AudioOutputDevice = "-" // Use "-" to pipe output to stdout
// 	case "linux":
// 		outputArgs["f"] = "pulse" // or "alsa"
// 	case "windows":
// 		outputArgs["f"] = "dshow"
// 	default:
// 		log.Fatalf("Unsupported OS for live audio capture: %s", runtime.GOOS)
// 	}

// 	return *p.options.AudioOutputDevice, outputArgs
// }

// // Start begins the audio playback. It launches the consumer (FFmpeg) and starts the producer loop.
// func (p *AudioPlayer) Start(input <-chan []float32) error {
// 	outputDevice, outputArgs := p.getArgs()

// 	// 1. Set up SHM and Semaphores for the player
// 	pid := os.Getpid()
// 	shmNameStr := fmt.Sprintf("goshadertoy_player_%d", pid)
// 	emptySemName := fmt.Sprintf("goshadertoy_player_empty_%d", pid)
// 	fullSemName := fmt.Sprintf("goshadertoy_player_full_%d", pid)

// 	// Clean up any old semaphores
// 	semaphore.RemoveSemaphore(emptySemName)
// 	semaphore.RemoveSemaphore(fullSemName)

// 	// This is approximate; the actual frame size depends on what ffmpeg capture sends.
// 	// 1024 samples * 1 channel * 4 bytes/sample (float32) = 4096 bytes. We use 4096 as a minimum.
// 	frameSize := 4096
// 	shmSize := int(unsafe.Sizeof(C.SHMControlBlock{})) + (frameSize * numAudioBuffers)

// 	var err error
// 	p.shm, err = sharedmemory.CreateSharedMemory(shmNameStr, shmSize)
// 	if err != nil {
// 		return fmt.Errorf("player failed to create shared memory: %w", err)
// 	}

// 	p.emptySem, err = semaphore.NewSemaphore(emptySemName, numAudioBuffers)
// 	if err != nil {
// 		p.shm.Close()
// 		return fmt.Errorf("player failed to create empty semaphore: %w", err)
// 	}

// 	p.fullSem, err = semaphore.NewSemaphore(fullSemName, 0)
// 	if err != nil {
// 		p.shm.Close()
// 		p.emptySem.Close()
// 		semaphore.RemoveSemaphore(emptySemName)
// 		return fmt.Errorf("player failed to create full semaphore: %w", err)
// 	}

// 	// 2. Configure and launch the FFmpeg consumer process
// 	pipeReader, pipeWriter := io.Pipe()
// 	p.pipeWriter = pipeWriter

// 	ffmpegCmd := ffmpeg.Input("pipe:", ffmpeg.KwArgs{"f": "shm_demuxer"}).
// 		Output(outputDevice, outputArgs).
// 		WithInput(pipeReader).
// 		ErrorToStdOut()

// 	if *p.options.FFMPEGPath != "" {
// 		ffmpegCmd.SetFfmpegPath(*p.options.FFMPEGPath)
// 	}

// 	p.cmd = ffmpegCmd.Compile()

// 	go func() {
// 		err := p.cmd.Run()
// 		if err != nil {
// 			log.Printf("FFmpeg audio player command finished with error: %v", err)
// 		}
// 		pipeWriter.Close()
// 	}()

// 	// 3. Write the SHMHeader to the pipe to initialize the demuxer in FFmpeg
// 	header := C.SHMHeader{
// 		frametype:   1, // Audio
// 		sample_rate: 44100,
// 		channels:    1,
// 		bit_depth:   32, // for pcm_f32le
// 		version:     1,
// 	}
// 	C.strncpy((*C.char)(unsafe.Pointer(&header.shm_file[0])), C.CString("/"+shmNameStr), 511)
// 	C.strncpy((*C.char)(unsafe.Pointer(&header.empty_sem_name[0])), C.CString(emptySemName), 255)
// 	C.strncpy((*C.char)(unsafe.Pointer(&header.full_sem_name[0])), C.CString(fullSemName), 255)

// 	headerBytes := (*[unsafe.Sizeof(header)]byte)(unsafe.Pointer(&header))[:]
// 	if _, err := pipeWriter.Write(headerBytes); err != nil {
// 		return fmt.Errorf("failed to write header to FFmpeg player: %w", err)
// 	}

// 	// 4. Start the producer goroutine
// 	go p.runProducer(input, frameSize)
// 	p.isStreaming = true

// 	return nil
// }

// // runProducer is the loop that takes audio data, writes it to shared memory, and notifies FFmpeg.
// func (p *AudioPlayer) runProducer(input <-chan []float32, frameSize int) {
// 	defer func() {
// 		// Signal EOF to the consumer
// 		eofHeader := C.FrameHeader{cmdtype: C.uint32_t(2)}
// 		eofHeaderBytes := (*[unsafe.Sizeof(eofHeader)]byte)(unsafe.Pointer(&eofHeader))[:]
// 		p.pipeWriter.Write(eofHeaderBytes)
// 		p.pipeWriter.Close()
// 	}()

// 	controlBlockPtr := (*C.SHMControlBlock)(p.shm.GetPtr())
// 	controlBlockPtr.num_buffers = numAudioBuffers
// 	controlBlockPtr.eof = 0

// 	var writeIndex uint32 = 0
// 	var pts int64 = 0

// 	for {
// 		select {
// 		case <-p.stopChan:
// 			return
// 		case data, ok := <-input:
// 			if !ok {
// 				return
// 			}

// 			if err := p.emptySem.Acquire(); err != nil {
// 				log.Printf("Player failed to acquire empty semaphore: %v", err)
// 				return
// 			}

// 			// Convert []float32 to []byte
// 			buf := new(bytes.Buffer)
// 			binary.Write(buf, binary.LittleEndian, data)
// 			byteData := buf.Bytes()

// 			writeOffset := int64(unsafe.Sizeof(C.SHMControlBlock{})) + (int64(writeIndex) * int64(frameSize))
// 			p.shm.WriteAt(byteData, writeOffset)

// 			frameHeader := C.FrameHeader{
// 				cmdtype: 0, // Audio Data
// 				size:    C.uint32_t(len(byteData)),
// 				pts:     C.int64_t(pts),
// 				offset:  C.uint64_t(writeOffset),
// 			}
// 			pts++

// 			frameHeaderBytes := (*[unsafe.Sizeof(frameHeader)]byte)(unsafe.Pointer(&frameHeader))[:]
// 			if _, err := p.pipeWriter.Write(frameHeaderBytes); err != nil {
// 				log.Printf("Player failed to write frame header: %v", err)
// 				return
// 			}

// 			writeIndex = (writeIndex + 1) % numAudioBuffers

// 			if err := p.fullSem.Release(); err != nil {
// 				log.Printf("Player failed to release full semaphore: %v", err)
// 				return
// 			}
// 		}
// 	}
// }

// // Stop terminates the audio playback.
// func (p *AudioPlayer) Stop() error {
// 	if !p.isStreaming {
// 		return nil
// 	}
// 	p.isStreaming = false
// 	close(p.stopChan)

// 	// Clean up SHM and semaphores
// 	if p.shm != nil {
// 		p.shm.Close()
// 	}
// 	if p.emptySem != nil {
// 		p.emptySem.Close()
// 		semaphore.RemoveSemaphore(fmt.Sprintf("goshadertoy_player_empty_%d", os.Getpid()))
// 	}
// 	if p.fullSem != nil {
// 		p.fullSem.Close()
// 		semaphore.RemoveSemaphore(fmt.Sprintf("goshadertoy_player_full_%d", os.Getpid()))
// 	}

// 	if p.cmd != nil && p.cmd.Process != nil {
// 		return p.cmd.Process.Kill()
// 	}
// 	return nil
// }

package audio

import (
	"fmt"
	"io"
	"log"
	"os/exec"
	"runtime"
	"unsafe"

	"github.com/richinsley/goshadertoy/options"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

// AudioPlayer plays audio data using FFmpeg.
type AudioPlayer struct {
	cmd        *exec.Cmd
	pipeWriter io.WriteCloser
	options    *options.ShaderOptions
}

// NewAudioPlayer creates a new audio player.
func NewAudioPlayer(options *options.ShaderOptions) (*AudioPlayer, error) {
	if *options.AudioOutputDevice == "" {
		return nil, fmt.Errorf("no audio output device specified")
	}

	p := &AudioPlayer{
		options: options,
	}

	return p, nil
}

func (p *AudioPlayer) getArgs() (outputDevice string, outputArgs ffmpeg.KwArgs) {
	outputArgs = ffmpeg.KwArgs{}
	switch runtime.GOOS {
	case "darwin":
		outputArgs["f"] = "audiotoolbox"
		outputArgs["audio_device_index"] = *p.options.AudioOutputDevice
		*p.options.AudioOutputDevice = "-" // Use "-" to pipe output to stdout
	case "linux":
		outputArgs["f"] = "pulse" // or "alsa"
	case "windows":
		outputArgs["f"] = "dshow"
	default:
		log.Fatalf("Unsupported OS for live audio capture: %s", runtime.GOOS)
	}

	return *p.options.AudioOutputDevice, outputArgs
}

// Start begins the audio playback.
func (p *AudioPlayer) Start(input <-chan []float32) error {
	outputDevice, outputArgs := p.getArgs()

	pipeReader, pipeWriter := io.Pipe()
	p.pipeWriter = pipeWriter

	ffmpegCmd := ffmpeg.Input("pipe:", ffmpeg.KwArgs{
		"f":  "f32le",
		"ar": "44100",
		"ac": "1",
	}).Output(outputDevice, outputArgs).WithInput(pipeReader).ErrorToStdOut()

	if *p.options.FFMPEGPath != "" {
		ffmpegCmd.SetFfmpegPath(*p.options.FFMPEGPath)
	}

	p.cmd = ffmpegCmd.Compile()

	go func() {
		err := p.cmd.Run()
		if err != nil {
			log.Printf("FFmpeg audio player command finished with error: %v", err)
		}
		pipeWriter.Close()
	}()

	go func() {
		for data := range input {
			// Convert []float32 to []byte
			byteData := make([]byte, len(data)*4)
			for i, v := range data {
				// Note: This assumes little-endian.
				// For cross-platform compatibility, you might need to handle endianness explicitly.
				b := *(*[4]byte)(unsafe.Pointer(&v))
				copy(byteData[i*4:], b[:])
			}
			_, err := p.pipeWriter.Write(byteData)
			if err != nil {
				log.Printf("Error writing to ffmpeg player pipe: %v", err)
				break
			}
		}
		p.pipeWriter.Close()
	}()

	return nil
}

// Stop terminates the audio playback.
func (p *AudioPlayer) Stop() error {
	if p.pipeWriter != nil {
		p.pipeWriter.Close()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		return p.cmd.Process.Kill()
	}
	return nil
}
