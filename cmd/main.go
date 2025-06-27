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

func runShadertoy(shaderArgs *api.ShaderArgs) {
	// Initialize renderer
	r, err := renderer.NewRenderer()
	if err != nil {
		log.Fatalf("Failed to create renderer: %v", err)
	}
	defer r.Shutdown()

	// Initialize the scene with shaders and channels
	err = r.InitScene(shaderArgs)
	if err != nil {
		log.Fatalf("Failed to initialize scene: %v", err)
	}

	// Start the render loop
	log.Println("Starting render loop...")
	r.Run()
}

func init() {
	runtime.LockOSThread()
}

func main() {
	// do this in init() for now
	// runtime.LockOSThread()

	var apikey = flag.String("apikey", "", "Shadertoy API key (from SHADERTOY_KEY env var if not set)")
	var shaderID = flag.String("shader", "XlSSzV", "Shadertoy shader ID (e.g., 'Another Cloudy Tunnel 2')") // Default to one with an image
	var help = flag.Bool("help", false, "Show help message")
	flag.Parse()

	if *help {
		fmt.Println("Shadertoy Shader Viewer (GLFW+go-gl version)")
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

	runShadertoy(shaderArgs)
}
