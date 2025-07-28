// audio/ffmpegbase.go
package audio

import (
	"bytes"
	"encoding/binary"
	"io"
	"log"
	"os/exec"
	"strings"
	"syscall"

	options "github.com/richinsley/goshadertoy/options"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

// ffmpegBaseDevice contains the common logic for all FFmpeg-based audio devices.
type ffmpegBaseDevice struct {
	cmd         *exec.Cmd
	pipeReader  io.ReadCloser
	audioChan   chan []float32
	stopChan    chan struct{}
	sampleRate  int
	isStreaming bool
	options     *options.ShaderOptions
	player      *AudioPlayer
}

// Start initiates the FFmpeg process and begins audio capture.
func (d *ffmpegBaseDevice) Start(inputArgs ffmpeg.KwArgs) (<-chan []float32, error) {
	d.audioChan = make(chan []float32, 16)

	pipeReader, pipeWriter := io.Pipe()
	d.pipeReader = pipeReader

	outputArgs := ffmpeg.KwArgs{
		"f":             "f32le",
		"c:a":           "pcm_f32le",
		"ar":            "44100",
		"ac":            "1",
		"flush_packets": "1",
	}

	var inputFile string
	if *d.options.AudioInputDevice != "" {
		inputFile = *d.options.AudioInputDevice
	} else {
		inputFile = *d.options.AudioInputFile
	}

	log.Printf("Starting FFmpeg audio capture for input: %s", inputFile)

	ffmpegNode := ffmpeg.Input(inputFile, inputArgs)
	ffmpegCmd := ffmpegNode.Output("pipe:", outputArgs).
		WithOutput(pipeWriter).ErrorToStdOut()

	if d.options.FFMPEGPath != nil && *d.options.FFMPEGPath != "" {
		ffmpegCmd.SetFfmpegPath(*d.options.FFMPEGPath)
	}

	d.cmd = ffmpegCmd.Compile()

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

// runAudioLoop reads raw audio data from the FFmpeg pipe and sends it to the output channels.
func (d *ffmpegBaseDevice) runAudioLoop(outputs ...chan<- []float32) {
	defer func() {
		log.Println("FFmpeg audio loop finished.")
	}()

	const bufferSize = 4096 // 1024 samples * 4 bytes/sample
	buffer := make([]byte, bufferSize)

	for {
		select {
		case <-d.stopChan:
			return
		default:
			n, err := io.ReadFull(d.pipeReader, buffer)
			if err != nil {
				if err != io.EOF && err != io.ErrUnexpectedEOF {
					log.Printf("Error reading from FFmpeg pipe: %v", err)
				}
				return
			}

			if n > 0 {
				floatData := make([]float32, n/4)
				reader := bytes.NewReader(buffer[:n])
				binary.Read(reader, binary.LittleEndian, &floatData)

				for _, out := range outputs {
					out <- floatData
				}
			}
		}
	}
}

// Stop terminates the FFmpeg process.
func (d *ffmpegBaseDevice) Stop() error {
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

// SampleRate returns the sample rate of the audio device.
func (d *ffmpegBaseDevice) SampleRate() int {
	return d.sampleRate
}
