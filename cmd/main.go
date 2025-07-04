package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"runtime"

	api "github.com/richinsley/goshadertoy/api"
	renderer "github.com/richinsley/goshadertoy/renderer"
)

func runShadertoy(shaderArgs *api.ShaderArgs, record bool, duration float64, fps int, width int, height int, outputFile string, ffmpegPath string) {
	// Initialize renderer
	// If recording, the window will be hidden (headless mode)
	r, err := renderer.NewRenderer(width, height, !record)
	if err != nil {
		log.Fatalf("Failed to create renderer: %v", err)
	}
	defer r.Shutdown()

	// Initialize the scene with shaders and channels
	err = r.InitScene(shaderArgs)
	if err != nil {
		log.Fatalf("Failed to initialize scene: %v", err)
	}

	if record {
		// Start the offscreen render loop
		log.Println("Starting offscreen render loop...")
		err = r.RunOffscreen(width, height, duration, fps, outputFile, ffmpegPath) // New call with width and height
		if err != nil {
			log.Fatalf("Offscreen rendering failed: %v", err)
		}
		log.Printf("Successfully rendered to %s", outputFile)
	} else {
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
	var apikey = flag.String("apikey", "", "Shadertoy API key (from SHADERTOY_KEY env var if not set)")
	var shaderID = flag.String("shader", "XlSSzV", "Shadertoy shader ID")
	var help = flag.Bool("help", false, "Show help message")

	// Recording flags
	var record = flag.Bool("record", false, "Enable recording mode")
	var duration = flag.Float64("duration", 10.0, "Duration to record in seconds")
	var fps = flag.Int("fps", 60, "Frames per second for recording")
	var width = flag.Int("width", 1280, "Width of the output")
	var height = flag.Int("height", 720, "Height of the output")
	var outputFile = flag.String("output", "output.mp4", "Output file name for recording")
	var ffmpegPath = flag.String("ffmpeg", "", "Path to ffmpeg executable")

	flag.Parse()

	if *help {
		fmt.Println("Shadertoy Shader Viewer/Recorder")
		flag.PrintDefaults()
		return
	}

	finalAPIKey := *apikey
	if finalAPIKey == "" {
		finalAPIKey = os.Getenv("SHADERTOY_KEY")
	}

	log.Printf("Fetching shader with ID: %s", *shaderID)
	shaderJSON, err := api.ShaderFromID(finalAPIKey, *shaderID, true)
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

	runShadertoy(shaderArgs, *record, *duration, *fps, *width, *height, *outputFile, *ffmpegPath)
}
