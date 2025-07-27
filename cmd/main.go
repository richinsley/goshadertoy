package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"

	api "github.com/richinsley/goshadertoy/api"
	arcana "github.com/richinsley/goshadertoy/arcana"
	options "github.com/richinsley/goshadertoy/options"
	renderer "github.com/richinsley/goshadertoy/renderer"
)

func runShadertoy(shaderArgs *api.ShaderArgs, options *options.ShaderOptions) {
	arcana.Init() // Initialize Arcana for FFmpeg support

	// Initialize renderer
	// If recording, the window will be hidden (headless mode)
	mode := *options.Mode
	isRecord := false
	if mode == "record" || mode == "stream" {
		isRecord = true
	}

	r, err := renderer.NewRenderer(*options.Width, *options.Height, !isRecord, *options.BitDepth, *options.NumPBOs)
	if err != nil {
		log.Fatalf("Failed to create renderer: %v", err)
	}
	defer r.Shutdown()

	// Initialize the scene with shaders and channels
	err = r.InitScene(shaderArgs, options)
	if err != nil {
		log.Fatalf("Failed to initialize scene: %v", err)
	}

	switch mode {
	case "record":
		// Start the offscreen render loop
		log.Println("Starting offscreen render loop...")
		err = r.RunOffscreen(options)
		if err != nil {
			log.Fatalf("Offscreen rendering failed: %v", err)
		}
		log.Printf("Successfully rendered to %s", *options.OutputFile)
	case "stream":
		// Add streaming logic here
		log.Println("Starting streaming mode...")
		err = r.RunOffscreen(options)
		if err != nil {
			log.Fatalf("Offscreen rendering failed: %v", err)
		}
	case "live":
		fallthrough
	default:
		// Start the interactive render loop
		log.Println("Starting interactive render loop...")
		r.Run()
	}
}

func init() {
	runtime.LockOSThread()
}

func main() {
	// Command-line flags
	options := &options.ShaderOptions{}
	options.APIKey = flag.String("apikey", "", "Shadertoy API key (from SHADERTOY_KEY env var if not set)")
	options.ShaderID = flag.String("shader", "XlSSzV", "Shadertoy shader ID")
	options.Help = flag.Bool("help", false, "Show help message")

	// Mode flag - replaces the record flag
	options.Mode = flag.String("mode", "Live", "Rendering mode: Live, Record, or Stream (case-insensitive)")
	options.Duration = flag.Float64("duration", 10.0, "Duration to record in seconds")
	options.FPS = flag.Int("fps", 60, "Frames per second for recording")
	options.Width = flag.Int("width", 1280, "Width of the output")
	options.Height = flag.Int("height", 720, "Height of the output")
	options.BitDepth = flag.Int("bitdepth", 8, "Bit depth for recording (8, 10, or 12)")
	options.OutputFile = flag.String("output", "output.mp4", "Output file name for recording")
	options.FFMPEGPath = flag.String("ffmpeg", "", "Path to ffmpeg executable")
	options.Codec = flag.String("codec", "h264", "Video codec for encoding: h264, hevc (default: h264)")
	options.DecklinkDevice = flag.String("decklink", "", "DeckLink device name for output")
	options.NumPBOs = flag.Int("numpbos", 2, "Number of PBOs to use for streaming")
	options.Prewarm = flag.Bool("prewarm", false, "Prewarm the renderer before recording/streaming (optional)")

	options.AudioInputDevice = flag.String("audio-input-device", "", "FFmpeg audio input device string (e.g., a file path or 'avfoundation:default'). Overrides default mic.")
	options.AudioInputFile = flag.String("audio-input-file", "", "FFmpeg audio input file (e.g., a WAV or MP3 file). Overrides default mic.")
	options.AudioOutputDevice = flag.String("audio-output-device", "", "FFmpeg audio output device string.")

	flag.Parse()

	if *options.Help {
		fmt.Println("Shadertoy Shader Viewer/Recorder")
		flag.PrintDefaults()
		return
	}

	// Validate mode (case-insensitive)
	*options.Mode = strings.ToLower(*options.Mode)
	validModes := map[string]bool{"live": true, "record": true, "stream": true}
	if !validModes[*options.Mode] {
		log.Fatalf("Invalid mode: %s. Valid modes are: Live, Record, Stream (case-insensitive)", *options.Mode)
	}

	// Validate codec
	*options.Codec = strings.ToLower(*options.Codec)
	validCodecs := map[string]bool{"h264": true, "hevc": true}
	if !validCodecs[*options.Codec] {
		log.Fatalf("Invalid codec: %s. Valid codecs are: h264, hevc", *options.Codec)
	}

	finalAPIKey := *options.APIKey
	if finalAPIKey == "" {
		finalAPIKey = os.Getenv("SHADERTOY_KEY")
	}

	log.Printf("Fetching shader with ID: %s", *options.ShaderID)
	shaderJSON, err := api.ShaderFromID(finalAPIKey, *options.ShaderID, true)
	if err != nil {
		log.Fatalf("Error fetching shader from ID: %v", err)
	}

	shaderArgs, err := api.ShaderArgsFromJSON(shaderJSON, true)
	if err != nil {
		log.Fatalf("Error processing shader JSON: %v", err)
	}
	log.Printf("Successfully processed shader: %s", shaderArgs.Title)

	if !shaderArgs.Complete {
		log.Println("Warning: Shader arguments may be incomplete (e.g., missing textures or unsupported inputs).")
	}

	runShadertoy(shaderArgs, options)
}
