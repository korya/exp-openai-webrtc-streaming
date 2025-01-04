package main

import (
	"fmt"
	"sync"

	"github.com/gordonklaus/portaudio"
	opusv2 "github.com/hraban/opus"
	"github.com/pion/webrtc/v4"
)

type PortaudioPlayer struct {
	stream      *portaudio.Stream
	opusDecoder *opusv2.Decoder
	mutex       sync.Mutex
	closed      bool
	buffer      []float32
	bufferIndex int
}

func NewPortaudioPlayer() (*PortaudioPlayer, error) {
	// Initialize PortAudio
	if err := portaudio.Initialize(); err != nil {
		return nil, fmt.Errorf("failed to initialize PortAudio: %w", err)
	}

	// Create Opus decoder
	decoder, err := opusv2.NewDecoder(48000, 2)
	if err != nil {
		portaudio.Terminate()
		return nil, fmt.Errorf("failed to create opus decoder: %w", err)
	}

	player := &PortaudioPlayer{
		opusDecoder: decoder,
		buffer:      make([]float32, 48000), // 1 second buffer
		bufferIndex: 0,
	}

	// Create and start PortAudio stream
	stream, err := portaudio.OpenDefaultStream(
		0,                   // input channels
		2,                   // output channels (stereo)
		48000,               // sample rate
		960,                 // frames per buffer (20ms at 48kHz)
		player.processAudio, // callback
	)
	if err != nil {
		portaudio.Terminate()
		return nil, fmt.Errorf("failed to open PortAudio stream: %w", err)
	}

	if err := stream.Start(); err != nil {
		stream.Close()
		portaudio.Terminate()
		return nil, fmt.Errorf("failed to start PortAudio stream: %w", err)
	}

	player.stream = stream
	return player, nil
}

func (ap *PortaudioPlayer) processAudio(out []float32) {
	ap.mutex.Lock()
	defer ap.mutex.Unlock()

	// Copy data from our buffer to the output
	for i := range out {
		if ap.bufferIndex >= len(ap.buffer) {
			ap.bufferIndex = 0
		}
		out[i] = ap.buffer[ap.bufferIndex]
		ap.bufferIndex++
	}
}

func (ap *PortaudioPlayer) WriteWebRTCTrack(track *webrtc.TrackRemote) error {
	// Buffer for decoded PCM data (48kHz stereo, 20ms frame = 960*2 samples)
	pcmBuf := make([]int16, 960*2)

	ap.stream.Start()

	for {
		if ap.closed {
			return nil
		}

		// Read RTP packet
		packet, _, err := track.ReadRTP()
		if err != nil {
			return fmt.Errorf("failed to read RTP packet: %w", err)
		}

		ap.mutex.Lock()
		// Decode Opus data to PCM
		samplesRead, err := ap.opusDecoder.Decode(packet.Payload, pcmBuf)
		if err != nil {
			ap.mutex.Unlock()
			fmt.Printf("Failed to decode opus data: %v\n", err)
			continue
		}

		// Convert int16 PCM to float32 and write to buffer
		for i := 0; i < samplesRead*2; i++ {
			bufferPos := (ap.bufferIndex + i) % len(ap.buffer)
			ap.buffer[bufferPos] = float32(pcmBuf[i]) / 32768.0
		}
		ap.mutex.Unlock()
	}
}

func (ap *PortaudioPlayer) Close() error {
	ap.mutex.Lock()
	defer ap.mutex.Unlock()

	if ap.closed {
		return nil
	}

	ap.closed = true

	if ap.stream != nil {
		if err := ap.stream.Stop(); err != nil {
			return fmt.Errorf("failed to stop stream: %w", err)
		}
		if err := ap.stream.Close(); err != nil {
			return fmt.Errorf("failed to close stream: %w", err)
		}
	}

	return portaudio.Terminate()
}

// Helper function to list available audio devices
func Portaudio_ListDevices() error {
	devices, err := portaudio.Devices()
	if err != nil {
		return fmt.Errorf("failed to get audio devices: %w", err)
	}

	fmt.Println("Available Audio Devices:")
	for _, device := range devices {
		fmt.Printf("Name: %s\n", device.Name)
		fmt.Printf("  MaxOutputChannels: %d\n", device.MaxOutputChannels)
		fmt.Printf("  DefaultSampleRate: %f\n", device.DefaultSampleRate)
		fmt.Printf("  DefaultLowOutputLatency: %f\n", device.DefaultLowOutputLatency)
		fmt.Printf("  DefaultHighOutputLatency: %f\n", device.DefaultHighOutputLatency)
		fmt.Println()
	}

	return nil
}
