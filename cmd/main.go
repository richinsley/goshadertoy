// richinsley/goshadertoy/goshadertoy-5ec5c80e8130811a9646c9bd6d1bbcbd07e1bb4d/cmd/main.go
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

func runShadertoy(shaderArgs *api.ShaderArgs, options *renderer.ShaderOptions) {
	// Initialize renderer
	// If recording, the window will be hidden (headless mode)
	r, err := renderer.NewRenderer(*options.Width, *options.Height, !*options.Record, *options.BitDepth)
	if err != nil {
		log.Fatalf("Failed to create renderer: %v", err)
	}
	defer r.Shutdown()

	// Initialize the scene with shaders and channels
	err = r.InitScene(shaderArgs)
	if err != nil {
		log.Fatalf("Failed to initialize scene: %v", err)
	}

	if *options.Record {
		// Start the offscreen render loop
		log.Println("Starting offscreen render loop...")
		err = r.RunOffscreen(options)
		if err != nil {
			log.Fatalf("Offscreen rendering failed: %v", err)
		}
		log.Printf("Successfully rendered to %s", *options.OutputFile)
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
	options := &renderer.ShaderOptions{}
	options.APIKey = flag.String("apikey", "", "Shadertoy API key (from SHADERTOY_KEY env var if not set)")
	options.ShaderID = flag.String("shader", "XlSSzV", "Shadertoy shader ID")
	options.Help = flag.Bool("help", false, "Show help message")

	// Recording flags
	options.Record = flag.Bool("record", false, "Enable recording mode")
	options.Duration = flag.Float64("duration", 10.0, "Duration to record in seconds")
	options.FPS = flag.Int("fps", 60, "Frames per second for recording")
	options.Width = flag.Int("width", 1280, "Width of the output")
	options.Height = flag.Int("height", 720, "Height of the output")
	options.BitDepth = flag.Int("bitdepth", 8, "Bit depth for recording (8, 10, or 12)")
	options.OutputFile = flag.String("output", "output.mp4", "Output file name for recording")
	options.FFMPEGPath = flag.String("ffmpeg", "", "Path to ffmpeg executable")
	options.DecklinkDevice = flag.String("decklink", "", "DeckLink device name for output")

	flag.Parse()

	if *options.Help {
		fmt.Println("Shadertoy Shader Viewer/Recorder")
		flag.PrintDefaults()
		return
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
