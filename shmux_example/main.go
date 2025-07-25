package main

import (
	"io"
	"log"
	"os"
	"strings"
	"unsafe"

	"github.com/richinsley/goshadertoy/semaphore"
	"github.com/richinsley/goshadertoy/sharedmemory"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

/*
#cgo CFLAGS: -I../shmframe
#include "protocol.h"
*/
import "C"

const (
	ffmpegArcanaPath = "./release/bin/ffmpeg_arcana"

	// IMPORTANT: This is platform-specific.
	// On macOS, use "default" or ":<device_index>" (e.g., ":0").
	// Find devices with: ffmpeg -f avfoundation -list_devices true -i ""
	// On Linux, use "hw:0" (for ALSA) or "default" (for PulseAudio).
	// On Windows, use "audio=<device_name>" (e.g., "audio=Microphone (Realtek High Definition Audio)").
	// Find devices with: ffmpeg -list_devices true -f dshow -i dummy
	audioDeviceName = ":0"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("Starting SHM Audio Device Capture Demo...")

	// --- FFmpeg Command Configuration for Live Device ---
	// var inputFormat string
	// switch runtime.GOOS {
	// case "darwin":
	// 	inputFormat = "avfoundation"
	// case "linux":
	// 	inputFormat = "pulse" // or "alsa"
	// case "windows":
	// 	inputFormat = "dshow"
	// default:
	// 	log.Fatalf("Unsupported OS for live audio capture: %s", runtime.GOOS)
	// }

	// Create an in-memory pipe.
	pipeReader, pipeWriter := io.Pipe()

	// Configure the ffmpeg command chain.
	// The new shm_muxer internally buffers to create fixed-size frames, so the `asetnsamples` filter is no longer needed.
	ffmpegNode := ffmpeg.Input("/Users/richardinsley/smoothcriminal.m4a")

	ffmpegCmd := ffmpegNode.Output("pipe:", ffmpeg.KwArgs{
		"f":                  "shm_muxer",
		"samples_per_buffer": "1024", // Match the muxer's default
		"c:a":                "pcm_f32le",
		"ac":                 "1",
		"ar":                 "44100",
		"flush_packets":      "1",
	}).
		WithOutput(pipeWriter).
		SetFfmpegPath(ffmpegArcanaPath).
		ErrorToStdOut()

	log.Printf("Executing FFmpeg command to capture from file...")

	// Run the ffmpeg command in a separate goroutine.
	go func() {
		err := ffmpegCmd.Run()
		if err != nil {
			log.Printf("FFmpeg command finished with error: %v", err)
		}
		pipeWriter.Close()
	}()

	log.Println("FFmpeg process started. Waiting for SHMHeader from pipe...")

	// --- Read the initial SHMHeader from the pipe ---
	var shmHeader C.SHMHeader
	headerSize := int(unsafe.Sizeof(shmHeader))
	headerBuf := make([]byte, headerSize)

	if _, err := io.ReadFull(pipeReader, headerBuf); err != nil {
		log.Fatalf("Failed to read SHMHeader from pipe: %v", err)
	}

	shmHeader = *(*C.SHMHeader)(unsafe.Pointer(&headerBuf[0]))

	// --- Extract info and open Semaphores and Shared Memory ---
	shmNameFromHeader := C.GoString(&shmHeader.shm_file[0])
	shmNameForGo := strings.TrimPrefix(shmNameFromHeader, "/")
	emptySemName := C.GoString(&shmHeader.empty_sem_name[0])
	fullSemName := C.GoString(&shmHeader.full_sem_name[0])

	log.Printf("Received SHMHeader:")
	log.Printf("  SHM Name: %s", shmNameForGo)
	log.Printf("  Empty Sem: %s", emptySemName)
	log.Printf("  Full Sem: %s", fullSemName)
	log.Printf("  SampleRate: %d, Channels: %d, BitDepth: %d", shmHeader.sample_rate, shmHeader.channels, shmHeader.bit_depth)

	emptySem, err := semaphore.OpenSemaphore(emptySemName)
	if err != nil {
		log.Fatalf("Failed to open empty semaphore '%s': %v", emptySemName, err)
	}
	defer emptySem.Close()

	fullSem, err := semaphore.OpenSemaphore(fullSemName)
	if err != nil {
		log.Fatalf("Failed to open full semaphore '%s': %v", fullSemName, err)
	}
	defer fullSem.Close()

	// --- Calculate SHM size ---
	const samplesPerBuffer = 1024 // This must match the `-samples_per_buffer` FFmpeg option.
	const numBuffers = 3          // This must match NUM_BUFFERS in the C code.
	bytesPerSample := int(shmHeader.bit_depth / 8)
	frameSize := samplesPerBuffer * int(shmHeader.channels) * bytesPerSample
	shmSize := int(unsafe.Sizeof(C.SHMControlBlock{})) + (frameSize * numBuffers)

	shm, err := sharedmemory.OpenSharedMemory(shmNameForGo, shmSize)
	if err != nil {
		log.Fatalf("Failed to open shared memory '%s': %v", shmNameForGo, err)
	}
	defer shm.Close()
	log.Printf("Opened shared memory segment '%s' with size %d bytes.", shmNameForGo, shm.GetSize())

	// --- Main loop to read frame headers and process data ---
	var frameHeader C.FrameHeader
	frameHeaderSize := int(unsafe.Sizeof(frameHeader))
	frameHeaderBuf := make([]byte, frameHeaderSize)
	frameCounter := 0

	log.Println("Starting to read audio frames from pipe...")
	f := "./ffmpeg_audio_output.raw"
	file, _ := os.Create(f)
	defer file.Close()
	for {
		_, err := io.ReadFull(pipeReader, frameHeaderBuf)
		if err != nil {
			if err == io.EOF {
				log.Println("Pipe closed by FFmpeg. All frames processed.")
				break
			}
			log.Fatalf("Error reading FrameHeader from pipe: %v", err)
		}

		frameHeader = *(*C.FrameHeader)(unsafe.Pointer(&frameHeaderBuf[0]))

		if frameHeader.cmdtype == 2 { // EOF from producer
			log.Println("Received explicit EOF command from FFmpeg. Finishing.")
			break
		}

		if frameHeader.cmdtype == 0 { // Audio Data
			if err := fullSem.Acquire(); err != nil {
				log.Fatalf("Error acquiring full semaphore: %v", err)
			}

			// The size in the frame header is now guaranteed to be the fixed frameSize.
			audioData := make([]byte, frameHeader.size)
			_, readErr := shm.ReadAt(audioData, int64(frameHeader.offset))
			if readErr != nil {
				log.Printf("Error reading audio data from shared memory: %v", readErr)
			} else {
				log.Printf("Frame %d (PTS %d): Read %d bytes of audio data from offset %d.", frameCounter, frameHeader.pts, len(audioData), frameHeader.offset)
			}

			if file != nil {
				if _, err := file.Write(audioData); err != nil {
					log.Printf("Failed to write to output file %s: %v", f, err)
				}
			}

			if err := emptySem.Release(); err != nil {
				log.Fatalf("Error releasing empty semaphore: %v", err)
			}

			frameCounter++
		}
	}

	log.Println("SHM Audio Client Demo finished.")
}
