package options

type ShaderOptions struct {
	APIKey            *string
	ShaderID          *string
	Help              *bool
	Mode              *string
	Duration          *float64
	FPS               *int
	Width             *int
	Height            *int
	BitDepth          *int
	OutputFile        *string
	DecklinkDevice    *string
	Codec             *string
	NumPBOs           *int
	Prewarm           *bool   // Optional prewarm flag to initialize the renderer before recording/streaming
	AudioInputDevice  *string // FFmpeg audio input device string (e.g., a file path or 'avfoundation:default'). Overrides default mic.
	AudioInputFile    *string // FFmpeg audio input file (e.g., a WAV or MP3 file). Overrides default mic.
	AudioOutputDevice *string // FFmpeg audio output device string.
	HasSoundShader    bool
	// Gamescope options
	GamescopeSocket          *string
	GamescopeTerminateOnExit *bool
}
