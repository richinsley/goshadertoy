package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"

	"github.com/go-gl/glfw/v3.3/glfw"
	api "github.com/richinsley/goshadertoy/api"
	arcana "github.com/richinsley/goshadertoy/arcana"
	audio "github.com/richinsley/goshadertoy/audio"
	glfwcontext "github.com/richinsley/goshadertoy/glfwcontext"
	graphics "github.com/richinsley/goshadertoy/graphics"
	headless "github.com/richinsley/goshadertoy/headless"
	options "github.com/richinsley/goshadertoy/options"
	renderer "github.com/richinsley/goshadertoy/renderer"
)

// gamescopeSessionResponse matches the response from the manager service.
type gamescopeSessionResponse struct {
	XDGRuntimeDir  string `json:"XDG_RUNTIME_DIR"`
	WaylandDisplay string `json:"WAYLAND_DISPLAY"`
	PID            int    `json:"pid"`
}

// setupGamescopeSession connects to the manager to start a session and configures the environment.
func setupGamescopeSession(options *options.ShaderOptions) {
	if options.GamescopeSocket == nil || *options.GamescopeSocket == "" {
		return // Not using gamescope.
	}
	if runtime.GOOS != "linux" {
		log.Println("Warning: Gamescope integration is only supported on Linux. Ignoring --gamescope-socket flag.")
		return
	}

	log.Println("Requesting Gamescope session from manager at", *options.GamescopeSocket)

	httpClient := http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", *options.GamescopeSocket)
			},
		},
	}

	sessionReq := map[string]interface{}{
		"width":            *options.Width,
		"height":           *options.Height,
		"hdr_enabled":      true, // This could be made a flag in the future
		"sdr_content_nits": 400,
		"fullscreen":       true,
		"fps":              *options.FPS,
	}
	reqBody, err := json.Marshal(sessionReq)
	if err != nil {
		log.Fatalf("Failed to marshal gamescope session request: %v", err)
	}

	resp, err := httpClient.Post("http://localhost/session/start", "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		log.Fatalf("Failed to start gamescope session: %v. Is the manager service running on a TTY?", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Fatalf("Error from gamescope manager: %s (%s)", resp.Status, string(body))
	}

	var sessionResp gamescopeSessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&sessionResp); err != nil {
		log.Fatalf("Failed to decode gamescope session response: %v", err)
	}

	// Set the environment for the current goshadertoy process.
	os.Setenv("XDG_RUNTIME_DIR", sessionResp.XDGRuntimeDir)
	os.Setenv("WAYLAND_DISPLAY", sessionResp.WaylandDisplay)
	os.Unsetenv("DISPLAY") // Ensure Wayland is prioritized

	log.Printf("Gamescope session started (PID: %d). Local environment configured.", sessionResp.PID)

	if options.GamescopeTerminateOnExit != nil && *options.GamescopeTerminateOnExit {
		log.Println("Will terminate gamescope session on exit.")
		// This deferred function will execute when runShadertoy returns.
		defer func() {
			log.Println("Terminating gamescope session...")
			resp, err := httpClient.Post("http://localhost/session/stop", "application/json", nil)
			if err != nil {
				log.Printf("Failed to stop gamescope session: %v", err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				log.Println("Gamescope session terminated successfully.")
			} else {
				body, _ := io.ReadAll(resp.Body)
				log.Printf("Error stopping gamescope session: %s (%s)", resp.Status, string(body))
			}
		}()
	}
}

func runShadertoy(initialShaderArgs *api.ShaderArgs, shaderIDs []string, options *options.ShaderOptions) {
	setupGamescopeSession(options)
	arcana.Init()

	mode := *options.Mode
	isRecord := mode == "record" || mode == "stream"

	var audioDevice audio.AudioDevice
	var err error
	soundSampleRate := 44100 // Default sample rate for audio playback
	// This channel connects the sound renderer (producer) to the audio feeder (consumer).
	preRenderedAudio := make(chan []float32, 4)

	// Determine if a sound shader is present
	_, options.HasSoundShader = initialShaderArgs.Buffers["sound"]
	if options.HasSoundShader {
		log.Println("Sound shader detected, using it as the primary audio source.")
		audioDevice, err = audio.NewShaderAudioDevice(options, preRenderedAudio, soundSampleRate)
		if err != nil {
			log.Fatalf("Failed to create shader audio device: %v", err)
		}
	} else {
		// If there's no sound shader, use an FFmpeg device or file input
		audioDevice, err = audio.NewFFmpegAudioDevice(options)
		if err != nil {
			log.Fatalf("Failed to create audio device: %v", err)
		}
	}
	defer audioDevice.Stop()

	// CONTEXT CREATION
	var visualContext, soundContext graphics.Context
	if isRecord && runtime.GOOS == "linux" { // For recording on Linux, use headless EGL contexts
		log.Println("Record mode on Linux: Using headless EGL contexts.")
		visualContext, err = headless.NewHeadless(*options.Width, *options.Height)
		if err != nil {
			log.Fatalf("Failed to create headless EGL context: %v", err)
		}
		if options.HasSoundShader {
			soundContext, err = headless.NewHeadless(1, 1) // Sound context can be minimal
			if err != nil {
				log.Fatalf("Failed to create headless sound context: %v", err)
			}
		}
	} else { // Otherwise, use a visible GLFW context
		log.Println("Using GLFW contexts.")
		if err := glfwcontext.InitGraphics(); err != nil {
			log.Fatalf("Failed to initialize graphics: %v", err)
		}
		defer glfwcontext.TerminateGraphics()

		visualContext, err = glfwcontext.New(options, !isRecord, nil)
		if err != nil {
			log.Fatalf("Failed to create visual GLFW context: %v", err)
		}
		if options.HasSoundShader {
			// Create a second, hidden, shared context for the sound renderer
			soundContext, err = glfwcontext.New(options, false, visualContext.GetWindow())
			if err != nil {
				log.Fatalf("Failed to create hidden sound context: %v", err)
			}
		}
	}

	// Create the scene-agnostic renderer
	r, err := renderer.NewRenderer(*options.Width, *options.Height, isRecord, *options.BitDepth, *options.NumPBOs, audioDevice, visualContext)
	if err != nil {
		log.Fatalf("Failed to create renderer: %v", err)
	}
	defer r.Shutdown()

	sceneCache := make(map[string]*renderer.Scene)
	sceneOrder := make([]string, 0, len(shaderIDs))
	var currentSceneIndex int = 0

	// The hardcoded list is gone. We now iterate over the `shaderIDs` slice passed into the function.
	for i, id := range shaderIDs {
		var argsToLoad *api.ShaderArgs
		// The arguments for the first shader are already loaded, so we can reuse them.
		if i == 0 {
			argsToLoad = initialShaderArgs
		} else {
			log.Printf("Loading scene for shader ID: %s", id)
			json, err := api.ShaderFromID("", id, true)
			if err != nil {
				log.Printf("Warning: Failed to fetch shader %s: %v", id, err)
				continue
			}
			argsToLoad, err = api.ShaderArgsFromJSON(json, true)
			if err != nil {
				log.Printf("Warning: Failed to process shader %s: %v", id, err)
				continue
			}
		}

		scene, err := r.LoadScene(argsToLoad, options)
		if err != nil {
			log.Printf("Warning: Failed to load scene for shader %s: %v", id, err)
			continue
		}
		sceneCache[id] = scene
		sceneOrder = append(sceneOrder, id)
	}

	if len(sceneOrder) == 0 {
		log.Fatalf("No scenes could be loaded. Exiting.")
	}

	// set the initial scene
	r.SetScene(sceneCache[sceneOrder[0]])

	// Register key callbacks for scene switching if we are in interactive mode
	if !isRecord {
		// Type assert the context to access the RegisterKeyCallback method
		if gctx, ok := visualContext.(*glfwcontext.Context); ok {
			for i := 0; i < len(sceneOrder) && i < 9; i++ { // Support keys 1 through 9
				sceneIndex := i // Capture the loop variable
				key := glfw.Key1 + glfw.Key(sceneIndex)

				gctx.RegisterKeyCallback(key, func() {
					if sceneIndex == currentSceneIndex {
						return // Don't switch to the same scene
					}

					sceneID := sceneOrder[sceneIndex]
					log.Printf("Switching to scene %d: %s ('%s')", sceneIndex+1, sceneID, sceneCache[sceneID].Title)

					previousScene := r.SetScene(sceneCache[sceneID])

					// IMPORTANT: Destroy the old scene to free up GPU resources
					if previousScene != nil {
						// previousScene.Destroy()
					}

					currentSceneIndex = sceneIndex
				})
			}
		}
	}

	// Start concurrent processes
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if options.HasSoundShader {
		// The sound renderer is tied to a specific shader's arguments
		soundRenderer := renderer.NewSoundShaderRenderer(soundContext, preRenderedAudio, initialShaderArgs, options)
		go func() {
			runtime.LockOSThread()
			if err := soundRenderer.InitGL(); err != nil {
				log.Fatalf("Failed to initialize sound renderer OpenGL: %v", err)
			}
			soundRenderer.Run(ctx)
		}()
	}

	// Start the audio device's own internal loop
	if err := audioDevice.Start(); err != nil {
		log.Fatalf("Failed to start audio device: %v", err)
	}

	// Run the main loop; Run() and RunOffscreen() will use the active scene set above
	switch mode {
	case "record", "stream":
		log.Printf("Starting %s mode...", mode)
		err = r.RunOffscreen(options)
		if err != nil {
			log.Fatalf("Offscreen rendering failed: %v", err)
		}
		log.Printf("Successfully rendered to %s", *options.OutputFile)
	default:
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
	options.ShaderID = flag.String("shader", "XlSSzV", "Shadertoy shader ID or a comma-separated list of IDs")
	options.Help = flag.Bool("help", false, "Show help message")
	options.Mode = flag.String("mode", "Live", "Rendering mode: Live, Record, or Stream (case-insensitive)")
	options.Duration = flag.Float64("duration", 10.0, "Duration to record in seconds")
	options.FPS = flag.Int("fps", 60, "Frames per second for recording")
	options.Width = flag.Int("width", 1280, "Width of the output")
	options.Height = flag.Int("height", 720, "Height of the output")
	options.BitDepth = flag.Int("bitdepth", 8, "Bit depth for recording (8, 10, or 12)")
	options.OutputFile = flag.String("output", "output.mp4", "Output file name for recording")
	options.Codec = flag.String("codec", "h264", "Video codec for encoding: h264, hevc (default: h264)")
	options.DecklinkDevice = flag.String("decklink", "", "DeckLink device name for output")
	options.NumPBOs = flag.Int("numpbos", 2, "Number of PBOs to use for streaming")
	options.Prewarm = flag.Bool("prewarm", false, "Prewarm the renderer before recording/streaming (optional)")

	options.AudioInputDevice = flag.String("audio-input-device", "", "FFmpeg audio input device string (e.g., a file path or 'avfoundation:default'). Overrides default mic.")
	options.AudioInputFile = flag.String("audio-input-file", "", "FFmpeg audio input file (e.g., a WAV or MP3 file). Overrides default mic.")
	options.AudioOutputDevice = flag.String("audio-output-device", "", "FFmpeg audio output device string.")

	options.GamescopeSocket = flag.String("gamescope-socket", "", "Path to the gamescope manager Unix socket. Enables running inside a managed gamescope session.")
	options.GamescopeTerminateOnExit = flag.Bool("gamescope-terminate-on-exit", false, "Terminate the gamescope session when goshadertoy exits.")

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

	// Parse the comma-separated shader ID list
	shaderIDs := strings.Split(*options.ShaderID, ",")
	if len(shaderIDs) == 0 || shaderIDs[0] == "" {
		log.Fatalf("No shader ID provided. Use the -shader flag to specify a single ID or a comma-separated list.")
	}
	// Trim any whitespace from user input
	for i := range shaderIDs {
		shaderIDs[i] = strings.TrimSpace(shaderIDs[i])
	}

	// Fetch the FIRST shader in the list to use for initialization.
	initialShaderID := shaderIDs[0]
	log.Printf("Fetching initial shader with ID: %s", initialShaderID)
	shaderJSON, err := api.ShaderFromID(finalAPIKey, initialShaderID, true)
	if err != nil {
		log.Fatalf("Error fetching initial shader %s: %v", initialShaderID, err)
	}

	initialShaderArgs, err := api.ShaderArgsFromJSON(shaderJSON, true)
	if err != nil {
		log.Fatalf("Error processing initial shader JSON: %v", err)
	}
	log.Printf("Successfully processed initial shader: %s", initialShaderArgs.Title)

	if !initialShaderArgs.Complete {
		log.Println("Warning: Initial shader arguments may be incomplete (e.g., missing textures or unsupported inputs).")
	}

	// Pass the initial parsed shader AND the full list of IDs to the run function.
	runShadertoy(initialShaderArgs, shaderIDs, options)
}
