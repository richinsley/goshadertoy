package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const (
	launcherSocketPath = "/tmp/gamescope.sock"
	signalerSocketDir  = "/tmp" // Directory for signaler sockets
	xdgRuntimeDir      = "/tmp/gamescope-runtime-fixed"
	waylandDisplay     = "gamescope-0"
)

// (SessionRequest and SessionResponse structs are unchanged)
type SessionRequest struct {
	Width          int  `json:"width"`
	Height         int  `json:"height"`
	HdrEnabled     bool `json:"hdr_enabled"`
	SdrContentNits int  `json:"sdr_content_nits"`
	Fullscreen     bool `json:"fullscreen"`
	FPS            int  `json:"fps,omitempty"`
}

type SessionResponse struct {
	XDGRuntimeDir  string `json:"XDG_RUNTIME_DIR"`
	WaylandDisplay string `json:"WAYLAND_DISPLAY"`
	PID            int    `json:"pid"`
}

type SessionState struct {
	mu        sync.Mutex
	cmd       *exec.Cmd
	isRunning bool
	readyChan chan struct{}
	// **NEW**: Path to the signaler's private control socket
	signalerSocketPath string
	// **NEW**: Channel to notify that the old process has terminated
	terminationNotifier chan struct{}
}

var currentSession SessionState

// --- Signaler Logic ---

// **NEW**: The signaler's terminate handler
func terminateHandler(w http.ResponseWriter, r *http.Request) {
	log.Println("Signaler: Received terminate request. Shutting down.")
	w.WriteHeader(http.StatusOK)
	// The flush is to ensure the HTTP response is sent before we exit.
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	// A brief pause to increase the likelihood of the response being sent.
	time.Sleep(50 * time.Millisecond)
	os.Exit(0)
}

func runSignaler() {
	// **MODIFIED**: Now expects 3 arguments: self, manager_socket, self_socket
	if len(os.Args) < 4 {
		log.Fatalf("Signaler internal error: Expected manager and self socket paths. Got: %v", os.Args)
	}
	managerSocketPath := os.Args[2]
	selfSocketPath := os.Args[3]

	// 1. Signal "ready" to the manager (same as before)
	log.Println("Signaler: Running inside gamescope. Notifying manager of readiness...")
	go func() {
		// Run in a goroutine to not block starting our own server.
		// Add a small delay for the manager to be ready to listen after process start.
		time.Sleep(200 * time.Millisecond)
		httpClient := http.Client{
			Transport: &http.Transport{
				Dial: func(_, _ string) (net.Conn, error) {
					return net.Dial("unix", managerSocketPath)
				},
			},
		}
		resp, err := httpClient.Post("http://localhost/session/ready", "application/json", nil)
		if err != nil {
			log.Printf("Signaler: Failed to send readiness signal to manager: %v", err)
			// If we can't signal ready, we are in a broken state. Exit.
			os.Exit(1)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			log.Printf("Signaler: Manager returned non-OK status for ready signal: %s", resp.Status)
			os.Exit(1)
		}
		log.Println("Signaler: Readiness signal sent successfully.")
	}()

	// 2. Start our own control server to listen for terminate commands
	if err := os.RemoveAll(selfSocketPath); err != nil {
		log.Fatalf("Signaler: Failed to remove existing socket: %v", err)
	}

	listener, err := net.Listen("unix", selfSocketPath)
	if err != nil {
		log.Fatalf("Signaler: Failed to create listener on %s: %v", selfSocketPath, err)
	}
	defer listener.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/terminate", terminateHandler)

	log.Printf("Signaler: Listening for control signals on %s", selfSocketPath)
	// This blocks forever, replacing the old `select {}`
	if err := http.Serve(listener, mux); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Signaler: Control server failed: %v", err)
	}
}

// --- Launcher Logic ---

func startSessionHandler(w http.ResponseWriter, r *http.Request) {
	var req SessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	currentSession.mu.Lock()

	// **NEW**: Logic to handle session restart
	if currentSession.isRunning {
		log.Println("Manager: Active session exists. Attempting to terminate it for restart.")

		// Create a temporary client to talk to the signaler's private socket
		signalerClient := http.Client{
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", currentSession.signalerSocketPath)
				},
			},
			Timeout: 5 * time.Second,
		}

		// Send the terminate request
		resp, err := signalerClient.Post("http://localhost/terminate", "application/json", nil)
		if err != nil {
			log.Printf("Manager: Failed to send terminate signal to existing session: %v. Attempting forceful kill.", err)
			currentSession.cmd.Process.Kill()
		} else {
			resp.Body.Close()
		}

		// Now, wait for the process reaping goroutine to confirm termination
		terminationChan := make(chan struct{})
		currentSession.terminationNotifier = terminationChan
		currentSession.mu.Unlock() // MUST unlock to allow the reaping goroutine to lock

		log.Println("Manager: Waiting for previous session to terminate...")
		select {
		case <-terminationChan:
			log.Println("Manager: Previous session terminated gracefully.")
		case <-time.After(10 * time.Second):
			log.Println("Manager: Timed out waiting for previous session to terminate. It may have been killed.")
			// The reaping goroutine might still be running, but we need to move on.
			// A forceful cleanup might be needed here in a production system.
			http.Error(w, "Timed out trying to stop the previous session", http.StatusInternalServerError)
			return
		}
		currentSession.mu.Lock() // Re-acquire the lock to continue
	}

	// From here, we are guaranteed no session is running.
	readyChan := make(chan struct{})
	currentSession.readyChan = readyChan

	// **NEW**: Generate a unique socket path for the new signaler instance
	signalerSocketPath := filepath.Join(signalerSocketDir, fmt.Sprintf("signaler-%d.sock", time.Now().UnixNano()))
	currentSession.signalerSocketPath = signalerSocketPath

	selfPath, err := os.Executable()
	if err != nil {
		currentSession.mu.Unlock()
		http.Error(w, "Could not determine own executable path", http.StatusInternalServerError)
		return
	}

	// **MODIFIED**: Pass the new private signaler socket path as an argument
	gamescopeArgs := buildGamescopeArgs(&req, selfPath, launcherSocketPath, signalerSocketPath)

	cmd := exec.Command("gamescope", gamescopeArgs...)
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, fmt.Sprintf("XDG_RUNTIME_DIR=%s", xdgRuntimeDir))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		currentSession.mu.Unlock()
		http.Error(w, fmt.Sprintf("Failed to start gamescope: %v", err), http.StatusInternalServerError)
		return
	}

	currentSession.cmd = cmd
	currentSession.isRunning = true

	// **MODIFIED**: The process reaping goroutine now handles the terminationNotifier
	go func(pid int, sockPath string) {
		cmd.Wait()
		log.Printf("Gamescope process PID %d has terminated.", pid)
		os.Remove(sockPath) // Clean up the signaler socket file

		currentSession.mu.Lock()
		defer currentSession.mu.Unlock()

		if currentSession.cmd != nil && currentSession.cmd.Process.Pid == pid {
			currentSession.isRunning = false
			currentSession.cmd = nil
			currentSession.signalerSocketPath = ""
			if currentSession.readyChan != nil {
				close(currentSession.readyChan)
				currentSession.readyChan = nil
			}
			// **NEW**: Notify the start handler if it's waiting for termination
			if currentSession.terminationNotifier != nil {
				close(currentSession.terminationNotifier)
				currentSession.terminationNotifier = nil
			}
		}
	}(cmd.Process.Pid, signalerSocketPath)

	// Unlock before waiting on the ready channel
	currentSession.mu.Unlock()

	log.Printf("Manager: Started gamescope (PID: %d). Waiting for ready signal...", cmd.Process.Pid)
	select {
	case <-readyChan:
		log.Println("Manager: Readiness confirmed. Responding to client.")
	case <-time.After(20 * time.Second):
		log.Println("Manager: Timed out waiting for ready signal from new session.")
		currentSession.mu.Lock()
		if currentSession.cmd != nil && currentSession.cmd.Process.Pid == cmd.Process.Pid {
			currentSession.cmd.Process.Kill()
		}
		currentSession.mu.Unlock()
		http.Error(w, "Timed out waiting for new gamescope session to become ready", http.StatusRequestTimeout)
		return
	}

	// Respond to the original client.
	resp := SessionResponse{
		XDGRuntimeDir:  xdgRuntimeDir,
		WaylandDisplay: waylandDisplay,
		PID:            cmd.Process.Pid,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// Helper to build gamescope arguments
func buildGamescopeArgs(req *SessionRequest, selfPath, managerSocket, signalerSocket string) []string {
	args := []string{
		"--backend", "drm", "--rt",
		"-W", fmt.Sprintf("%d", req.Width),
		"-H", fmt.Sprintf("%d", req.Height),
	}
	if req.HdrEnabled {
		args = append(args, "--hdr-enabled", "--hdr-sdr-content-nits", fmt.Sprintf("%d", req.SdrContentNits))
	}
	if req.Fullscreen {
		args = append(args, "-f")
	}
	if req.FPS > 0 {
		args = append(args, "-r", fmt.Sprintf("%d", req.FPS))
	}
	args = append(args, "--", selfPath, "signaler", managerSocket, signalerSocket)
	return args
}

// The stop handler can remain as a forceful backup
func stopSessionHandler(w http.ResponseWriter, r *http.Request) {
	currentSession.mu.Lock()
	defer currentSession.mu.Unlock()

	if !currentSession.isRunning {
		http.Error(w, "No session is currently running", http.StatusNotFound)
		return
	}
	log.Println("Manager: Received stop request. Terminating session via SIGTERM.")
	if err := currentSession.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		http.Error(w, fmt.Sprintf("Failed to stop gamescope process: %v", err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "Gamescope session stop signal sent.")
}

// readyHandler is unchanged
func readyHandler(w http.ResponseWriter, r *http.Request) {
	currentSession.mu.Lock()
	defer currentSession.mu.Unlock()

	if !currentSession.isRunning {
		http.Error(w, "No session is running to be marked as ready", http.StatusNotFound)
		return
	}
	if currentSession.readyChan != nil {
		log.Println("Manager: Received readiness signal from signaler.")
		close(currentSession.readyChan)
		currentSession.readyChan = nil
	}
	w.WriteHeader(http.StatusOK)
}

func runLauncher() {
	socketPath := launcherSocketPath
	flag.StringVar(&socketPath, "socket", launcherSocketPath, "Path to the Unix socket for the service.")
	flag.Parse()

	if err := os.MkdirAll(xdgRuntimeDir, 0700); err != nil {
		log.Fatalf("Failed to create XDG_RUNTIME_DIR: %v", err)
	}
	if err := os.RemoveAll(socketPath); err != nil {
		log.Fatalf("Failed to remove existing socket file: %v", err)
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Fatalf("Failed to create unix socket listener: %v", err)
	}
	defer listener.Close()

	if err := os.Chmod(socketPath, 0777); err != nil {
		log.Fatalf("Failed to set permissions on socket file: %v", err)
	}

	log.Printf("Server listening on unix socket: %s", socketPath)

	mux := http.NewServeMux()
	mux.HandleFunc("/session/start", startSessionHandler)
	mux.HandleFunc("/session/ready", readyHandler)
	mux.HandleFunc("/session/stop", stopSessionHandler)

	if err := http.Serve(listener, mux); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "signaler" {
		runSignaler()
	} else {
		runLauncher()
	}
}
