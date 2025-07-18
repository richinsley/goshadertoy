package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"log"
	"os"
	"os/exec"
	"runtime"
	"unsafe"

	"github.com/richinsley/goshadertoy/sharedmemory" // Assuming this path is correct for your project
)

// SHMHeader and FrameHeader structs must exactly match the C definitions in shmframe/protocol.h
// This is critical for correct binary parsing.
// [cite: richinsley/goshadertoy/shmframe/protocol.h]
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
	// Replace with the actual path to your ffmpeg-arcana binary
	// You might need to adjust this based on where your build script installs it.
	ffmpegArcanaPath = "/Users/richardinsley/Projects/arcana/release/bin/ffmpeg_arcana" // Common install path for build_ffmpeg_arcana.sh
	audioFileName    = "/Users/richardinsley/smoothcriminal.m4a"
	shmName          = "/goshadertoy_audio_shm_demo" // Unique name for this demo
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("Starting SHM Audio Client Demo...")

	// --- 1. Prepare and run the ffmpeg-arcana command ---
	// This command decodes scrim.m4a and sends raw audio frames to shared memory
	// and control packets to stdout (which will be piped to this Go program's stdin).
	ffmpegArgs := []string{
		"-i", audioFileName,
		"-f", "shm_muxer",
		"-shm_name", shmName,
		// Output control packets to stdout (pipe:1)
		"pipe:1",
	}

	cmd := exec.Command(ffmpegArcanaPath, ffmpegArgs...)
	log.Printf("Executing FFmpeg command: %s %v", ffmpegArcanaPath, ffmpegArgs)

	// Set cmd.Stdout to the current process's Stdin.
	// This is the crucial part for piping FFmpeg's stdout (control packets)
	// directly into our Go program's stdin.
	cmd.Stdout = os.Stdin
	// Explicitly set Stderr to the current process's Stderr for debugging FFmpeg errors
	cmd.Stderr = os.Stderr

	// We don't need to capture Stderr explicitly for this demo,
	// FFmpeg's logs will go to the console.
	// cmd.Stderr = os.Stderr

	// Start the FFmpeg process
	err := cmd.Start()
	if err != nil {
		log.Fatalf("Failed to start FFmpeg process: %v", err)
	}
	log.Println("FFmpeg process started. Waiting for SHMHeader...")

	// --- 2. Read SHMHeader from FFmpeg's stdout (our stdin) ---
	var shmHeader SHMHeader
	shmHeaderBuf := make([]byte, unsafe.Sizeof(shmHeader))

	// Read from os.Stdin (which is connected to FFmpeg's stdout)
	n, err := io.ReadFull(os.Stdin, shmHeaderBuf)
	if err != nil {
		log.Fatalf("Failed to read SHMHeader from stdin: %v", err)
	}
	if n != int(unsafe.Sizeof(shmHeader)) {
		log.Fatalf("Incomplete SHMHeader read: expected %d bytes, got %d", unsafe.Sizeof(shmHeader), n)
	}

	// Unmarshal the bytes into the SHMHeader struct
	buf := bytes.NewReader(shmHeaderBuf)
	err = binary.Read(buf, binary.LittleEndian, &shmHeader)
	if err != nil {
		log.Fatalf("Failed to parse SHMHeader: %v", err)
	}

	log.Printf("Received SHMHeader:")
	log.Printf("  SHM File: %s", string(shmHeader.ShmFile[:bytes.IndexByte(shmHeader.ShmFile[:], 0)]))
	log.Printf("  Version: %d", shmHeader.Version)
	log.Printf("  Frame Type: %d (0=Video, 1=Audio)", shmHeader.FrameType)
	log.Printf("  Sample Rate: %d", shmHeader.SampleRate)
	log.Printf("  Channels: %d", shmHeader.Channels)
	log.Printf("  Bit Depth: %d", shmHeader.BitDepth)
	log.Printf("  Pixel Format: %d", shmHeader.PixFmt) // For audio, this is AV_SAMPLE_FMT_*

	if shmHeader.FrameType != 1 {
		log.Fatalf("Expected audio stream (FrameType 1), but got %d. Exiting.", shmHeader.FrameType)
	}

	// --- 3. Open the shared memory segment ---
	// The shm_muxer automatically creates and links the SHM. We just open it.
	shm, err := sharedmemory.OpenSharedMemory(shmName, int(shmHeader.SampleRate*shmHeader.Channels*(shmHeader.BitDepth/8)*2)) // Approximate size for 2 seconds of audio
	if err != nil {
		log.Fatalf("Failed to open shared memory '%s': %v", shmName, err)
	}
	defer func() {
		shm.Close()
		log.Println("Closed shared memory.")
	}()
	log.Printf("Opened shared memory segment '%s' with size %d bytes.", shmName, shm.GetSize())

	// --- 4. Read FrameHeaders and Audio Data in a loop ---
	var frameHeader FrameHeader
	frameHeaderBuf := make([]byte, unsafe.Sizeof(frameHeader))
	frameCounter := 0

	log.Println("Starting to read audio frames...")
	for {
		// Read FrameHeader from stdin
		n, err := io.ReadFull(os.Stdin, frameHeaderBuf)
		if err != nil {
			if err == io.EOF {
				log.Println("Received EOF from FFmpeg pipe. All frames processed.")
				break
			}
			log.Fatalf("Error reading FrameHeader from stdin: %v", err)
		}
		if n != int(unsafe.Sizeof(frameHeader)) {
			log.Fatalf("Incomplete FrameHeader read: expected %d bytes, got %d", unsafe.Sizeof(frameHeader), n)
		}

		// Unmarshal the bytes into the FrameHeader struct
		buf = bytes.NewReader(frameHeaderBuf)
		err = binary.Read(buf, binary.LittleEndian, &frameHeader)
		if err != nil {
			log.Fatalf("Failed to parse FrameHeader: %v", err)
		}

		if frameHeader.CmdType == 2 { // EOF command
			log.Println("Received explicit EOF command from FFmpeg. Finishing.")
			break
		} else if frameHeader.CmdType == 0 { // Data frame
			if int(frameHeader.Size) > shm.GetSize() {
				log.Printf("Frame %d (PTS %d): Data size %d exceeds SHM buffer size %d. Skipping.",
					frameCounter, frameHeader.Pts, frameHeader.Size, shm.GetSize())
				continue
			}

			// Read audio data from shared memory
			audioData := make([]byte, frameHeader.Size)
			_, err = shm.ReadAt(audioData, 0) // Read from offset 0, as FFmpeg writes to the start
			if err != nil {
				log.Printf("Frame %d (PTS %d): Error reading audio data from SHM: %v. Skipping.",
					frameCounter, frameHeader.Pts, err)
				continue
			}

			// Process the audio data (e.g., print size, first few samples)
			log.Printf("Frame %d (PTS %d): Received %d bytes of audio data.", frameCounter, frameHeader.Pts, len(audioData))

			// Example: Convert first 10 samples to float32 (assuming 16-bit signed PCM for simplicity)
			if shmHeader.BitDepth == 16 && len(audioData) >= 20 {
				samples := make([]float32, 10)
				for i := 0; i < 10; i++ {
					val := int16(binary.LittleEndian.Uint16(audioData[i*2 : i*2+2]))
					samples[i] = float32(val) / float32(1<<15) // Normalize to -1.0 to 1.0
				}
				log.Printf("  First 10 samples (normalized): %v...", samples)
			} else if shmHeader.BitDepth == 32 && len(audioData) >= 40 {
				samples := make([]float32, 10)
				for i := 0; i < 10; i++ {
					val := binary.LittleEndian.Uint32(audioData[i*4 : i*4+4])
					samples[i] = *(*float32)(unsafe.Pointer(&val)) // Direct cast for float32
				}
				log.Printf("  First 10 samples (float32): %v...", samples)
			}

			frameCounter++
		} else {
			log.Printf("Received unknown command type: %d. Skipping.", frameHeader.CmdType)
		}
	}

	// --- 5. Wait for FFmpeg process to finish ---
	err = cmd.Wait()
	if err != nil {
		log.Printf("FFmpeg process finished with error: %v", err)
	} else {
		log.Println("FFmpeg process finished successfully.")
	}

	log.Println("SHM Audio Client Demo finished.")
}

// init ensures that the current process's stdin is not closed by default,
// which is important when it's being used as a pipe.
func init() {
	if runtime.GOOS == "windows" {
		// On Windows, os.Stdin is a *File, which is fine.
		// On Unix-like systems, os.Stdin is usually a *File, but can be a pipe.
		// No special handling needed here for os.Stdin itself,
		// but rather how the parent process (shell) connects to it.
	}
}
