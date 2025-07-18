package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"log"
	"runtime"
	"strings"
	"unsafe"

	"github.com/richinsley/goshadertoy/sharedmemory"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

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

const (
	ffmpegArcanaPath = "/Users/richardinsley/Projects/pyshadertranslator/release/bin/ffmpeg_arcana"
	audioFileName    = "/Users/richardinsley/smoothcriminal.m4a"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("Starting SHM Audio Client Demo...")

	// Create an in-memory pipe. The FFmpeg process will write to pipeWriter,
	// and our main Go routine will read from pipeReader.
	pipeReader, pipeWriter := io.Pipe()

	// Configure the ffmpeg command using the ffmpeg-go library.
	ffmpegCmd := ffmpeg.Input(audioFileName).
		// Add the explicit output codec to give FFmpeg a hint.
		// We use pcm_f32le which corresponds to 32-bit float, a common high-quality raw format.
		// Your shm_muxer already handles this format.
		Output("pipe:", ffmpeg.KwArgs{
			"f":   "shm_muxer",
			"c:a": "pcm_f32le", // This is the crucial fix.
		}).
		WithOutput(pipeWriter).
		SetFfmpegPath(ffmpegArcanaPath).
		ErrorToStdOut()

	log.Printf("Executing FFmpeg command...")

	// Run the ffmpeg command in a separate goroutine.
	// It will start writing to the pipeWriter as soon as it's ready.
	go func() {
		err := ffmpegCmd.Run()
		if err != nil {
			log.Printf("FFmpeg command finished with error: %v", err)
		}
		// When FFmpeg is done (or fails), close the writer. This will unblock
		// the reader in the main goroutine and signal that there's no more data.
		pipeWriter.Close()
	}()

	log.Println("FFmpeg process started. Waiting for SHMHeader from pipe...")

	// --- The rest of the logic is the same, but reads from pipeReader ---

	var shmHeader SHMHeader
	shmHeaderBuf := make([]byte, unsafe.Sizeof(shmHeader))

	// Read the header from the pipe. This will block until FFmpeg writes it.
	n, err := io.ReadFull(pipeReader, shmHeaderBuf)
	if err != nil {
		log.Fatalf("Failed to read SHMHeader from pipe: %v", err)
	}
	if n != int(unsafe.Sizeof(shmHeader)) {
		log.Fatalf("Incomplete SHMHeader read: expected %d bytes, got %d", unsafe.Sizeof(shmHeader), n)
	}

	buf := bytes.NewReader(shmHeaderBuf)
	err = binary.Read(buf, binary.LittleEndian, &shmHeader)
	if err != nil {
		log.Fatalf("Failed to parse SHMHeader: %v", err)
	}

	shmNameFromHeader := string(shmHeader.ShmFile[:bytes.IndexByte(shmHeader.ShmFile[:], 0)])
	shmNameForGo := strings.TrimPrefix(shmNameFromHeader, "/")

	log.Printf("Received SHMHeader:")
	log.Printf("  Dynamically generated SHM File: %s", shmNameFromHeader)
	//... (other logs)

	shm, err := sharedmemory.OpenSharedMemory(shmNameForGo, int(shmHeader.SampleRate*shmHeader.Channels*(shmHeader.BitDepth/8)*2))
	if err != nil {
		log.Fatalf("Failed to open shared memory '%s': %v", shmNameForGo, err)
	}
	defer shm.Close()
	log.Printf("Opened shared memory segment '%s' with size %d bytes.", shmNameForGo, shm.GetSize())

	var frameHeader FrameHeader
	frameHeaderBuf := make([]byte, unsafe.Sizeof(frameHeader))
	frameCounter := 0

	log.Println("Starting to read audio frames from pipe...")
	for {
		// Read the next frame header from the pipe
		_, err := io.ReadFull(pipeReader, frameHeaderBuf)
		if err != nil {
			if err == io.EOF {
				log.Println("Pipe closed by FFmpeg. All frames processed.")
				break
			}
			log.Fatalf("Error reading FrameHeader from pipe: %v", err)
		}

		buf = bytes.NewReader(frameHeaderBuf)
		binary.Read(buf, binary.LittleEndian, &frameHeader)

		if frameHeader.CmdType == 2 {
			log.Println("Received explicit EOF command from FFmpeg. Finishing.")
			break
		} else if frameHeader.CmdType == 0 {
			// (Frame processing logic remains the same)
			audioData := make([]byte, frameHeader.Size)
			shm.ReadAt(audioData, 0)
			allzero := true
			for _, b := range audioData {
				if b != 0 {
					allzero = false
					break
				}
			}
			if allzero {
				log.Printf("Frame %d (PTS %d): Received all-zero audio data, skipping frame.", frameCounter, frameHeader.Pts)
				continue
			}
			log.Printf("Frame %d (PTS %d): Received %d bytes of audio data.", frameCounter, frameHeader.Pts, len(audioData))
			frameCounter++
		}
	}

	log.Println("SHM Audio Client Demo finished.")
}

func init() {
	if runtime.GOOS == "windows" {
	}
}
