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

	"github.com/richinsley/goshadertoy/sharedmemory"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

// SHMControlBlock must exactly match the C definition.
type SHMControlBlock struct {
	WriteIndex uint32
	ReadIndex  uint32
	NumBuffers uint32
	EOF        uint32
}

// SHMHeader and FrameHeader structs must exactly match the C definitions.
type SHMHeader struct {
	ShmFile    [512]byte
	Version    uint32
	FrameType  uint32 // 0 for video, 1 for audio
	FrameRate  uint32
	Channels   uint32
	SampleRate uint32
	BitDepth   uint32
	Width      uint32
	Height     uint32
	PixFmt     int32
}

type FrameHeader struct {
	CmdType uint32 // 0 for data, 2 for EOF
	Size    uint32
	Pts     int64
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
	sampleRate  int
	isStreaming bool
}

// NewFFmpegAudioDevice creates a new audio device that sources its audio from an FFmpeg process.
// The `inputFile` can be a file path or any valid FFmpeg input string (e.g., "avfoundation:default").
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
		ErrorToStdOut() // Add this to match the working example and handle stderr.

	if d.ffmpegPath != "" {
		ffmpegCmd.SetFfmpegPath(d.ffmpegPath)
	}

	d.cmd = ffmpegCmd.Compile()

	// Now, run the command object we just created and stored.
	go func() {
		err := d.cmd.Run()
		if err != nil {
			// Don't log expected errors when we interrupt the process
			if !strings.Contains(err.Error(), "signal: killed") {
				log.Printf("FFmpeg command finished with error: %v", err)
			}
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

	// Open the shared memory segment
	shmNameFromHeader := string(shmHeader.ShmFile[:bytes.IndexByte(shmHeader.ShmFile[:], 0)])
	shmNameForGo := strings.TrimPrefix(shmNameFromHeader, "/")

	// Calculate a safe buffer size, giving it some extra room.
	frameSize := int(shmHeader.SampleRate*shmHeader.Channels*(shmHeader.BitDepth/8)) * 2
	if frameSize < 4096 {
		frameSize = 4096
	}
	shmSize := int(unsafe.Sizeof(SHMControlBlock{})) + (frameSize * 3) // 3 buffers

	shm, err := sharedmemory.OpenSharedMemory(shmNameForGo, shmSize)
	if err != nil {
		log.Fatalf("Failed to open shared memory '%s': %v", shmNameForGo, err)
		return
	}
	d.shm = shm

	controlBlockPtr := (*SHMControlBlock)(d.shm.GetPtr())

	// Continuously read audio frames
	var frameHeader FrameHeader
	frameHeaderSize := int(unsafe.Sizeof(frameHeader))
	frameHeaderBuf := make([]byte, frameHeaderSize)
	totalRead := int64(0)

	for {
		// This loop is now driven by blocking reads from the pipe.
		// It will only proceed when FFmpeg sends a header or closes the pipe.
		select {
		case <-d.stopChan:
			log.Println("Stop signal received, exiting audio loop.")
			return
		default:
			// This is now a blocking read. The goroutine will sleep here until data is available.
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
				log.Println("Received EOF command from FFmpeg. Total bytes read:", totalRead)
				return
			}

			if frameHeader.CmdType == 0 { // Audio Data
				// We have a header, so a frame's data should be in the ring buffer.
				// This check is a safeguard.
				if controlBlockPtr.ReadIndex == controlBlockPtr.WriteIndex {
					log.Println("Warning: Received a data header but the ring buffer is empty. Skipping.")
					continue
				}

				if frameHeader.Size == 0 {
					controlBlockPtr.ReadIndex = (controlBlockPtr.ReadIndex + 1) % controlBlockPtr.NumBuffers
					continue
				}

				readOffset := int64(unsafe.Sizeof(SHMControlBlock{})) + (int64(controlBlockPtr.ReadIndex) * int64(frameSize))
				audioData := make([]byte, frameHeader.Size)
				n, err := d.shm.ReadAt(audioData, readOffset)
				if err != nil || n != int(frameHeader.Size) {
					log.Printf("Error reading audio data from shared memory: %v", err)
					controlBlockPtr.ReadIndex = (controlBlockPtr.ReadIndex + 1) % controlBlockPtr.NumBuffers
					continue
				}
				totalRead += int64(n)

				// Convert byte slice to float32 slice
				floatData := make([]float32, len(audioData)/4) // 4 bytes per float32
				reader := bytes.NewReader(audioData)
				binary.Read(reader, binary.LittleEndian, &floatData)
				d.audioChan <- floatData

				// Signal that we've consumed the frame by advancing the read pointer.
				controlBlockPtr.ReadIndex = (controlBlockPtr.ReadIndex + 1) % controlBlockPtr.NumBuffers
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
