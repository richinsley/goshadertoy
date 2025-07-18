package audio

import (
	"bytes"
	"encoding/binary"
	"io"
	"log"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"

	"github.com/richinsley/goshadertoy/semaphore"
	"github.com/richinsley/goshadertoy/sharedmemory"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

// SHMHeader and FrameHeader structs must exactly match the C definitions.
type SHMHeader struct {
	ShmFile      [512]byte
	EmptySemName [256]byte
	FullSemName  [256]byte
	Version      uint32
	FrameType    uint32 // 0 for video, 1 for audio
	FrameRate    uint32
	Channels     uint32
	SampleRate   uint32
	BitDepth     uint32
	Width        uint32
	Height       uint32
	PixFmt       int32
}

type FrameHeader struct {
	CmdType uint32 // 0 for data, 2 for EOF
	Size    uint32
	Pts     int64
	Offset  uint64 // The exact byte offset for the frame
}

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
}

// NewFFmpegAudioDevice creates a new audio device that sources its audio from an FFmpeg process.
func NewFFmpegAudioDevice(inputFile, ffmpegPath string) *FFmpegAudioDevice {
	return &FFmpegAudioDevice{
		inputFile:  inputFile,
		ffmpegPath: ffmpegPath,
		stopChan:   make(chan struct{}),
	}
}

func (d *FFmpegAudioDevice) Start() (<-chan []float32, error) {
	d.audioChan = make(chan []float32, 16) // Buffered channel

	pipeReader, pipeWriter := io.Pipe()
	d.pipeReader = pipeReader

	log.Printf("Starting FFmpeg audio capture for input: %s", d.inputFile)

	ffmpegCmd := ffmpeg.Input(d.inputFile).
		Output("pipe:", ffmpeg.KwArgs{
			"f":   "shm_muxer",
			"c:a": "pcm_f32le",
		}).
		WithOutput(pipeWriter).
		ErrorToStdOut()

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

	var shmHeader SHMHeader
	headerSize := int(unsafe.Sizeof(shmHeader))
	headerBuf := make([]byte, headerSize)

	// Read the SHMHeader from FFmpeg's output pipe
	if _, err := io.ReadFull(d.pipeReader, headerBuf); err != nil {
		log.Printf("Failed to read SHMHeader from FFmpeg pipe: %v", err)
		return
	}

	buf := bytes.NewReader(headerBuf)
	if err := binary.Read(buf, binary.LittleEndian, &shmHeader); err != nil {
		log.Printf("Failed to parse SHMHeader: %v", err)
		return
	}

	d.sampleRate = int(shmHeader.SampleRate)
	log.Printf("Received SHMHeader from FFmpeg: SampleRate=%d, Channels=%d", d.sampleRate, shmHeader.Channels)

	shmNameFromHeader := string(shmHeader.ShmFile[:bytes.IndexByte(shmHeader.ShmFile[:], 0)])
	shmNameForGo := strings.TrimPrefix(shmNameFromHeader, "/")
	emptySemName := string(shmHeader.EmptySemName[:bytes.IndexByte(shmHeader.EmptySemName[:], 0)])
	fullSemName := string(shmHeader.FullSemName[:bytes.IndexByte(shmHeader.FullSemName[:], 0)])

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
	frameSize := int(shmHeader.SampleRate*shmHeader.Channels*(shmHeader.BitDepth/8)) * 2
	if frameSize < 4096 {
		frameSize = 4096
	}
	shmSize := int(unsafe.Sizeof(shmHeader)) + (frameSize * 3) // numBuffers = 3

	d.shm, err = sharedmemory.OpenSharedMemory(shmNameForGo, shmSize)
	if err != nil {
		log.Printf("Failed to open shared memory '%s': %v", shmNameForGo, err)
		return
	}

	var frameHeader FrameHeader
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

			buf = bytes.NewReader(frameHeaderBuf)
			binary.Read(buf, binary.LittleEndian, &frameHeader)

			if frameHeader.CmdType == 2 { // EOF
				log.Println("Received EOF command from FFmpeg.")
				return
			}

			if frameHeader.CmdType == 0 { // Audio Data
				if err := d.fullSem.Acquire(); err != nil {
					log.Printf("Error acquiring full semaphore: %v", err)
					return
				}

				audioData := make([]byte, frameHeader.Size)
				n, err := d.shm.ReadAt(audioData, int64(frameHeader.Offset))
				if err != nil || n != int(frameHeader.Size) {
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
