package api

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	// Blank imports for image decoders so image.Decode can handle them.
	_ "image/jpeg"
	_ "image/png"
)

const (
	shadertoyAPIURL   = "https://www.shadertoy.com/api/v1"
	shadertoyMediaURL = "https://www.shadertoy.com"
)

// VolumeData holds the parsed data and metadata for a 3D volume texture.
type VolumeData struct {
	Width       uint32
	Height      uint32
	Depth       uint32
	NumChannels uint8
	Layout      uint8  // Currently unused, but parsed for completeness
	Format      uint16 // 0 for I8, 10 for F32
	Data        []byte
}

// Global client with a custom User-Agent header.
var httpClient = &http.Client{
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	},
}

type headerTransport struct {
	Transport http.RoundTripper
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", "https://github.com/gemini/go-shadertoy-client")
	return t.Transport.RoundTrip(req)
}

func init() {
	httpClient.Transport = &headerTransport{Transport: http.DefaultTransport}
}

// --- Structs for Shadertoy API Response ---

type ShadertoyResponse struct {
	Shader *Shader `json:"Shader"`
	Error  string  `json:"Error,omitempty"`
	IsAPI  bool    `json:"isAPI,omitempty"` // Indicates if this is an API response
}

type Shader struct {
	Info       ShaderInfo   `json:"info"`
	RenderPass []RenderPass `json:"renderpass"`
}

type ShaderInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

type RenderPass struct {
	Inputs  []Input  `json:"inputs"`
	Outputs []Output `json:"outputs"`
	Code    string   `json:"code"`
	Name    string   `json:"name"`
	Type    string   `json:"type"`
}

type Input struct {
	Channel int     `json:"channel"`
	CType   string  `json:"ctype"`
	Src     string  `json:"src"`
	Sampler Sampler `json:"sampler"`
}

type Output struct {
	Id      int `json:"id"`
	Channel int `json:"channel"`
}

type Sampler struct {
	Filter   string `json:"filter"`
	Wrap     string `json:"wrap"`
	VFlip    string `json:"vflip"`
	SRGB     string `json:"srgb"`
	Internal string `json:"internal"`
}

// raw shader data is ever so slightly different from the API response.
type rawShaderResponse []rawShader

type rawShader struct {
	Info          ShaderInfo      `json:"info"`
	RawRenderPass []rawRenderPass `json:"renderpass"`
}

type rawRenderPass struct {
	Inputs  []rawInput  `json:"inputs"`
	Outputs []rawOutput `json:"outputs"`
	Code    string      `json:"code"`
	Name    string      `json:"name"`
	Type    string      `json:"type"`
}

type rawInput struct {
	Id          string  `json:"id"`
	Filepath    string  `json:"filepath"`
	PreviewFile string  `json:"previewfilepath"`
	Type        string  `json:"type"`
	Channel     int     `json:"channel"`
	Sampler     Sampler `json:"sampler"`
	Published   int     `json:"published"`
}

type rawOutput struct {
	Id      string `json:"id"`
	Channel int    `json:"channel"`
}

func rawShaderToShader(raw rawShader) *Shader {
	shader := &Shader{
		Info:       raw.Info,
		RenderPass: make([]RenderPass, len(raw.RawRenderPass)),
	}

	for i, rPass := range raw.RawRenderPass {
		shader.RenderPass[i] = RenderPass{
			Inputs:  make([]Input, len(rPass.Inputs)),
			Outputs: make([]Output, len(rPass.Outputs)),
			Code:    rPass.Code,
			Name:    rPass.Name,
			Type:    rPass.Type,
		}
		for j, inp := range rPass.Inputs {
			shader.RenderPass[i].Inputs[j] = Input{
				Channel: inp.Channel,
				CType:   inp.Type,
				Src:     inp.Filepath, // Use Filepath for raw inputs
				Sampler: inp.Sampler,
			}
		}
		for j, out := range rPass.Outputs {
			shader.RenderPass[i].Outputs[j] = Output{
				// Id:      out.Id,
				Channel: out.Channel,
			}
		}
	}
	return shader
}

// --- Structs for Processed Shader Data ---

// ShadertoyChannel represents a generic input channel.
type ShadertoyChannel struct {
	CType     string
	Channel   int
	Sampler   Sampler
	Data      image.Image    // For textures
	Volume    *VolumeData    // For 3D volume textures
	CubeData  [6]image.Image // For cubemaps
	BufferRef string         // Buffer name that will be attached to this input channel
}

// BufferRenderPass represents a processed buffer pass.
type BufferRenderPass struct {
	Code      string
	Inputs    []*ShadertoyChannel
	BufferIdx string
}

// ShaderArgs holds the final, processed arguments for a Shadertoy implementation.
type ShaderArgs struct {
	ShaderCode string
	CommonCode string
	Inputs     []*ShadertoyChannel
	Buffers    map[string]*BufferRenderPass
	Title      string
	Complete   bool
}

type ShaderPasses map[string]*ShaderArgs

// getAPIKey retrieves the Shadertoy API key from the environment and validates it.
func getAPIKey() (string, error) {
	key := os.Getenv("SHADERTOY_KEY")
	if key == "" {
		return "", fmt.Errorf("SHADERTOY_KEY environment variable not set. See https://www.shadertoy.com/howto#q2")
	}

	// Validate the key
	testURL := fmt.Sprintf("%s/shaders/query/test?key=%s", shadertoyAPIURL, key)
	req, err := http.NewRequest("GET", testURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create API key test request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("API key test request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to use ShaderToy API with key, status code: %d", resp.StatusCode)
	}

	var apiError ShadertoyResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiError); err != nil {
		return "", fmt.Errorf("failed to decode API key test response: %w", err)
	}

	if apiError.Error != "" {
		return "", fmt.Errorf("failed to use ShaderToy API with key: %s", apiError.Error)
	}

	return key, nil
}

// getCacheDir determines the appropriate OS-specific cache directory.
func getCacheDir(subdir string) (string, error) {
	var baseCacheDir string
	var err error

	switch runtime.GOOS {
	case "windows":
		baseCacheDir = os.Getenv("LOCALAPPDATA")
		if baseCacheDir == "" {
			err = fmt.Errorf("LOCALAPPDATA environment variable not set")
		}
	case "darwin":
		homeDir := os.Getenv("HOME")
		if homeDir == "" {
			err = fmt.Errorf("HOME environment variable not set")
		} else {
			baseCacheDir = filepath.Join(homeDir, "Library", "Caches")
		}
	default: // linux, bsd, etc.
		baseCacheDir = os.Getenv("XDG_CACHE_HOME")
		if baseCacheDir == "" {
			homeDir := os.Getenv("HOME")
			if homeDir == "" {
				err = fmt.Errorf("HOME environment variable not set")
			} else {
				baseCacheDir = filepath.Join(homeDir, ".cache")
			}
		}
	}

	if err != nil {
		return "", err
	}

	cacheDir := filepath.Join(baseCacheDir, "shadertoy", subdir)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create cache directory at %s: %w", cacheDir, err)
	}

	return cacheDir, nil
}

// downloadMediaChannels processes input descriptions, downloading textures as needed.
func downloadMediaChannels(inputs []Input, passType string, useCache bool) ([]*ShadertoyChannel, bool, error) {
	channels := make([]*ShadertoyChannel, 4)
	complete := true

	cacheDir, err := getCacheDir("media")
	if err != nil {
		return nil, false, fmt.Errorf("could not get cache directory: %w", err)
	}

	for _, inp := range inputs {
		channel := &ShadertoyChannel{
			CType:   inp.CType,
			Channel: inp.Channel,
			Sampler: inp.Sampler,
		}

		switch inp.CType {
		case "texture":
			mediaURL := shadertoyMediaURL + inp.Src
			cachePath := filepath.Join(cacheDir, filepath.Base(inp.Src))

			var img image.Image

			if useCache {
				if f, err := os.Open(cachePath); err == nil {
					img, _, err = image.Decode(f)
					f.Close()
					if err != nil {
						log.Printf("Warning: could not decode cached image %s: %v. Redownloading...", cachePath, err)
						// Fall through to download
					}
				}
			}

			if img == nil { // Not cached or cache read failed
				resp, err := httpClient.Get(mediaURL)
				if err != nil {
					return nil, false, fmt.Errorf("failed to download media %s: %w", mediaURL, err)
				}
				defer resp.Body.Close()

				if resp.StatusCode != http.StatusOK {
					return nil, false, fmt.Errorf("failed to load media %s, status code: %d", mediaURL, resp.StatusCode)
				}

				// Read into a buffer to allow both decoding and saving
				data, err := io.ReadAll(resp.Body)
				if err != nil {
					return nil, false, fmt.Errorf("failed to read media data from %s: %w", mediaURL, err)
				}

				img, _, err = image.Decode(strings.NewReader(string(data)))
				if err != nil {
					return nil, false, fmt.Errorf("failed to decode downloaded image from %s: %w", mediaURL, err)
				}

				if useCache {
					if err := os.WriteFile(cachePath, data, 0644); err != nil {
						log.Printf("Warning: failed to save media to cache at %s: %v", cachePath, err)
					}
				}
			}
			channel.Data = img

		case "buffer":
			// Buffer inputs have a path of the form '/media/previz/buffer00.png'
			// Remove file extension
			nameWithoutExt := strings.TrimSuffix(inp.Src, filepath.Ext(inp.Src))

			// Get last two characters
			lastTwo := nameWithoutExt[len(nameWithoutExt)-2:]

			// Convert to int
			num, err := strconv.Atoi(lastTwo)
			if err != nil {
				log.Printf("invalid buffer reference in src: %s", inp.Src)
				complete = false
			} else {
				switch num {
				case 0:
					channel.BufferRef = "A"
				case 1:
					channel.BufferRef = "B"
				case 2:
					channel.BufferRef = "C"
				case 3:
					channel.BufferRef = "D"
				default:
					log.Printf("invalid buffer reference in src: %s", inp.Src)
					complete = false
				}
			}
		case "volume":
			mediaURL := shadertoyMediaURL + inp.Src
			cachePath := filepath.Join(cacheDir, filepath.Base(inp.Src))
			var volumeDataBytes []byte

			if useCache {
				if data, err := os.ReadFile(cachePath); err == nil {
					volumeDataBytes = data
				}
			}

			if volumeDataBytes == nil { // Not cached or cache read failed
				resp, err := httpClient.Get(mediaURL)
				if err != nil {
					return nil, false, fmt.Errorf("failed to download volume %s: %w", mediaURL, err)
				}
				defer resp.Body.Close()

				if resp.StatusCode != http.StatusOK {
					return nil, false, fmt.Errorf("failed to load volume %s, status code: %d", mediaURL, resp.StatusCode)
				}

				data, err := io.ReadAll(resp.Body)
				if err != nil {
					return nil, false, fmt.Errorf("failed to read volume data from %s: %w", mediaURL, err)
				}
				volumeDataBytes = data

				if useCache {
					if err := os.WriteFile(cachePath, data, 0644); err != nil {
						log.Printf("Warning: failed to save volume to cache at %s: %v", cachePath, err)
					}
				}
			}

			if len(volumeDataBytes) < 20 {
				return nil, false, fmt.Errorf("volume data for channel %d is too small (size: %d)", inp.Channel, len(volumeDataBytes))
			}

			// Parse the 20-byte header from the .bin file
			reader := bytes.NewReader(volumeDataBytes)
			vol := &VolumeData{}

			var signature uint32 // First 4 bytes are a signature
			binary.Read(reader, binary.LittleEndian, &signature)
			binary.Read(reader, binary.LittleEndian, &vol.Width)
			binary.Read(reader, binary.LittleEndian, &vol.Height)
			binary.Read(reader, binary.LittleEndian, &vol.Depth)
			binary.Read(reader, binary.LittleEndian, &vol.NumChannels)
			binary.Read(reader, binary.LittleEndian, &vol.Layout)
			binary.Read(reader, binary.LittleEndian, &vol.Format)

			// The rest of the byte slice is the raw texture data.
			vol.Data = volumeDataBytes[20:]
			channel.Volume = vol
			log.Printf("Parsed Volume for Channel %d: %dx%dx%d", inp.Channel, vol.Width, vol.Height, vol.Depth)
		case "cubemap":
			var images [6]image.Image
			completeDownload := true
			for i := 0; i < 6; i++ {
				var mediaURL string
				if i == 0 {
					mediaURL = shadertoyMediaURL + inp.Src
				} else {
					n := strings.LastIndex(inp.Src, ".")
					if n == -1 {
						return nil, false, fmt.Errorf("could not determine file extension for cubemap: %s", inp.Src)
					}
					mediaURL = shadertoyMediaURL + inp.Src[:n] + "_" + fmt.Sprintf("%d", i) + inp.Src[n:]
				}

				cachePath := filepath.Join(cacheDir, filepath.Base(mediaURL))

				var img image.Image
				if useCache {
					if f, err := os.Open(cachePath); err == nil {
						img, _, err = image.Decode(f)
						f.Close()
						if err != nil {
							log.Printf("Warning: could not decode cached image %s: %v. Redownloading...", cachePath, err)
						}
					}
				}

				if img == nil {
					resp, err := httpClient.Get(mediaURL)
					if err != nil {
						log.Printf("Warning: failed to download cubemap face %s: %v", mediaURL, err)
						completeDownload = false
						continue
					}
					defer resp.Body.Close()

					if resp.StatusCode != http.StatusOK {
						log.Printf("Warning: failed to load cubemap face %s, status code: %d", mediaURL, resp.StatusCode)
						completeDownload = false
						continue
					}
					data, err := io.ReadAll(resp.Body)
					if err != nil {
						log.Printf("Warning: failed to read media data from %s: %w", mediaURL, err)
						completeDownload = false
						continue
					}
					img, _, err = image.Decode(strings.NewReader(string(data)))
					if err != nil {
						log.Printf("Warning: failed to decode downloaded image from %s: %w", mediaURL, err)
						completeDownload = false
						continue
					}
					if useCache {
						if err := os.WriteFile(cachePath, data, 0644); err != nil {
							log.Printf("Warning: failed to save media to cache at %s: %v", cachePath, err)
						}
					}
				}
				images[i] = img
			}

			// The Shadertoy cubemap source seems to have the Top (+Y) and Bottom (-Y)
			// faces swapped compared to the strict OpenGL enum order.
			// OpenGL expects: index 2 = +Y (Top), index 3 = -Y (Bottom).
			// We swap them here to match what OpenGL expects.
			if images[2] != nil && images[3] != nil {
				images[2], images[3] = images[3], images[2]
			}

			if !completeDownload {
				complete = false
			}
			channel.CubeData = images
		case "mic":
			// For microphone input, we don't download anything, just create a placeholder channel.
		default:
			log.Printf("Warning: unsupported input type '%s'", inp.CType)
			complete = false
			continue
		}

		if inp.Channel >= 0 && inp.Channel < 4 {
			channels[inp.Channel] = channel
		}
	}

	return channels, complete, nil
}

// ShaderFromID fetches a shader's JSON data from Shadertoy.com by its ID.
func ShaderFromID(apikey string, idOrURL string, useCache bool) (*ShadertoyResponse, error) {
	if useCache {
		// If using cache, we should check if the shader is already cached.
		cacheDir, err := getCacheDir("shaders")
		if err != nil {
			return nil, fmt.Errorf("could not get cache directory: %w", err)
		}
		cachePath := filepath.Join(cacheDir, idOrURL+".json")
		// "/Users/richardinsley/Library/Caches/shadertoy/shaders/tfKSz3.json"
		if _, err := os.Stat(cachePath); err == nil {
			// Shader is cached, read from file
			data, err := os.ReadFile(cachePath)
			if err != nil {
				return nil, fmt.Errorf("failed to read cached shader file %s: %w", cachePath, err)
			}
			var shaderResp ShadertoyResponse
			if err := json.Unmarshal(data, &shaderResp); err != nil {
				return nil, fmt.Errorf("failed to decode cached shader JSON: %w", err)
			}
			if shaderResp.Error != "" {
				return nil, fmt.Errorf("cached shader has error: %s", shaderResp.Error)
			}
			if shaderResp.Shader == nil {
				return nil, fmt.Errorf("cached shader JSON is invalid: 'Shader' key is missing")
			}
			return &shaderResp, nil
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to check cached shader file %s: %w", cachePath, err)
		}
	}

	// If not cached, fetch from the API.
	// Ensure the cache directory exists for media downloads.
	cacheDir, err := getCacheDir("media")
	if useCache && err != nil {
		return nil, fmt.Errorf("could not get cache directory: %w", err)
	}

	log.Printf("Using cache directory: %s\n", cacheDir)

	if apikey == "" {
		apikey, err = getAPIKey()
		if err != nil {
			return nil, err
		}
	}

	shaderID := idOrURL
	if strings.Contains(shaderID, "/") {
		shaderID = filepath.Base(strings.TrimSuffix(shaderID, "/"))
	}

	apiURL := fmt.Sprintf("%s/shaders/%s", shadertoyAPIURL, shaderID)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	q := req.URL.Query()
	q.Add("key", apikey)
	req.URL.RawQuery = q.Encode()

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request to shadertoy API failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to load shader %s, status code: %d", shaderID, resp.StatusCode)
	}

	var shaderResp ShadertoyResponse
	// get the bytes from the response body
	bodyBytes, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(bodyBytes, &shaderResp); err != nil {
		return nil, fmt.Errorf("failed to decode shader JSON: %w", err)
	}

	if shaderResp.Error != "" {
		// try a raw request instead
		log.Printf("Warning: Shadertoy API error for %s: %s (is it public+api?)", shaderID, shaderResp.Error)
		rawData, err := GetRawAPIShaderData(shaderID)

		if err != nil {
			return nil, fmt.Errorf("failed to fetch raw shader data for %s: %w", shaderID, err)
		}
		var rawResp rawShaderResponse
		if err := json.Unmarshal([]byte(rawData), &rawResp); err != nil {
			return nil, fmt.Errorf("failed to decode raw shader JSON: %w", err)
		}
		if len(rawResp) == 0 {
			return nil, fmt.Errorf("raw shader response is empty for %s", shaderID)
		}
		// Convert raw response to ShadertoyResponse
		nshader := rawShaderToShader(rawResp[0])
		shaderResp = ShadertoyResponse{
			Shader: nshader, // Use the first shader in the raw response
			IsAPI:  false,   // Mark this as a raw response
		}
	} else {
		shaderResp.IsAPI = true // Mark this as an API response
	}

	if shaderResp.Shader == nil {
		return nil, fmt.Errorf("invalid JSON response: 'Shader' key is missing")
	}

	// write shaderResp to cache if using cache
	if useCache {
		cacheDir, err := getCacheDir("shaders")
		if err != nil {
			return nil, fmt.Errorf("could not get cache directory: %w", err)
		}
		cachePath := filepath.Join(cacheDir, shaderID+".json")
		data, err := json.Marshal(shaderResp)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal shader for cache: %w", err)
		}
		if err := os.WriteFile(cachePath, data, 0644); err != nil {
			return nil, fmt.Errorf("failed to write shader to cache at %s: %w", cachePath, err)
		}
		log.Printf("Shader %s cached at %s", shaderID, cachePath)
	}
	return &shaderResp, nil
}

// ShaderArgsFromJSON builds the final ShaderArgs from the raw API response.
func ShaderArgsFromJSON(shaderData *ShadertoyResponse, useCache bool) (*ShaderArgs, error) {
	args := &ShaderArgs{
		Inputs:   make([]*ShadertoyChannel, 4),
		Buffers:  map[string]*BufferRenderPass{},
		Complete: true,
	}

	if shaderData.Shader == nil {
		return nil, fmt.Errorf("shader data must have a 'Shader' key")
	}

	var inputsComplete bool
	var err error

	for _, rPass := range shaderData.Shader.RenderPass {
		switch rPass.Type {
		case "image":
			args.ShaderCode = rPass.Code
			if len(rPass.Inputs) > 0 {
				args.Inputs, inputsComplete, err = downloadMediaChannels(rPass.Inputs, rPass.Type, useCache)
				if err != nil {
					return nil, fmt.Errorf("error processing image pass inputs: %w", err)
				}
				args.Complete = args.Complete && inputsComplete
			}
		case "common":
			args.CommonCode = rPass.Code
		case "buffer":
			// The buffer index ('A', 'B', 'C', 'D') is usually the last character of the name.
			if rPass.Name == "" {
				return nil, fmt.Errorf("buffer pass has no name, cannot determine index")
			}
			bufferIdx := strings.ToUpper(rPass.Name[len(rPass.Name)-1:])

			bufferInputs, inputsComplete, err := downloadMediaChannels(rPass.Inputs, rPass.Type, useCache)
			if err != nil {
				return nil, fmt.Errorf("error processing buffer %s inputs: %w", bufferIdx, err)
			}
			args.Complete = args.Complete && inputsComplete

			bufferPass := &BufferRenderPass{
				Code:      rPass.Code,
				Inputs:    bufferInputs,
				BufferIdx: bufferIdx,
			}
			args.Buffers[bufferIdx] = bufferPass

		default:
			log.Printf("Warning: unsupported render pass type: %s", rPass.Type)
			args.Complete = false
		}
	}

	info := shaderData.Shader.Info
	args.Title = fmt.Sprintf(`"%s" by %s`, info.Name, info.Username)

	return args, nil
}
