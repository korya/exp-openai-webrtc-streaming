package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-audio/audio"
	"github.com/go-audio/wav"
	"github.com/gordonklaus/portaudio"
)

const (
	sampleRate      = 48000
	channels        = 1
	frameDurationMs = 20
	framesPerBuffer = (sampleRate * frameDurationMs) / 1000 // 960 samples for 20ms at 48kHz
)

const echoDelay = 300 * time.Millisecond

func main() {
	// 1) Initialize PortAudio
	if err := portaudio.Initialize(); err != nil {
		log.Fatalf("Failed to initialize PortAudio: %v", err)
	}
	defer portaudio.Terminate()

	// 2) Create a WAV file to record the raw microphone feed
	wavFile, err := os.Create("recorded_mic.wav")
	if err != nil {
		log.Fatalf("Failed to create wav file: %v", err)
	}
	defer wavFile.Close()

	// Set up the WAV encoder
	enc := wav.NewEncoder(
		wavFile,
		sampleRate,
		16, // bit depth
		channels,
		1, // WAV format (1 = PCM)
	)
	defer enc.Close()

	// We'll store our raw PCM data in a buffer, then encode it chunk by chunk
	// The echo is done separately, so the WAV file will not have the echo added.
	echoChan := make(chan []float32, 100)
	micBuffer := make([]float32, framesPerBuffer)

	// 3) Open default input (microphone)
	inStream, err := portaudio.OpenDefaultStream(
		channels, // 1 input channel
		0,        // no output
		float64(sampleRate),
		framesPerBuffer,
		micBuffer,
	)
	if err != nil {
		log.Fatalf("Failed to open input stream: %v", err)
	}
	defer inStream.Close()

	// 4) Open default output (speaker) for echo
	outStream, err := portaudio.OpenDefaultStream(
		0,        // no input
		channels, // 1 output channel
		float64(sampleRate),
		framesPerBuffer,
		func(out []float32) {
			// Attempt to read one frame from echoChan
			select {
			case delayedFrame := <-echoChan:
				copy(out, delayedFrame)
				for i := len(delayedFrame); i < len(out); i++ {
					out[i] = 0
				}
			default:
				// No data => output silence
				for i := range out {
					out[i] = 0
				}
			}
		},
	)
	if err != nil {
		log.Fatalf("Failed to open output stream: %v", err)
	}
	defer outStream.Close()

	// 5) Start streams
	if err := inStream.Start(); err != nil {
		log.Fatalf("Failed to start input stream: %v", err)
	}
	if err := outStream.Start(); err != nil {
		log.Fatalf("Failed to start output stream: %v", err)
	}

	// Create an OS signal channel
	sigChan := make(chan os.Signal, 1)
	// Notify on Interrupt (Ctrl+C) and SIGTERM
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		// Wait for a signal
		<-sigChan
		log.Println("Caught Ctrl+C or SIGTERM, closing WAV file and streams...")

		// Stop PortAudio streams
		inStream.Stop()
		outStream.Stop()

		// Properly close the WAV encoder/file
		enc.Close()
		wavFile.Close()

		// Terminate PortAudio
		portaudio.Terminate()

		os.Exit(0)
	}()

	// 6) Goroutine: read mic frames, record them to WAV, schedule echo
	go func() {
		log.Println("Starting microphone capture + WAV record loop...")

		for {
			if err := inStream.Read(); err != nil {
				if errors.Is(err, portaudio.InputOverflowed) {
					log.Println("PortAudio input overflowed, ignoring")
					continue
				}
				log.Printf("Error reading mic data: %v\n", err)
				return
			}

			// Convert float32 -> int (in 16-bit range) for wav encoding
			intBuf := make([]int, len(micBuffer))
			for i, sample := range micBuffer {
				v := int(sample * 32767)
				if v < -32768 {
					v = -32768
				} else if v > 32767 {
					v = 32767
				}
				intBuf[i] = v
			}

			// Prepare an AudioBuffer
			audioBuf := &audio.IntBuffer{
				Format:         &audio.Format{NumChannels: channels, SampleRate: sampleRate},
				SourceBitDepth: 16,
				Data:           intBuf,
			}
			// Write to the WAV encoder
			if err := enc.Write(audioBuf); err != nil {
				log.Printf("Error writing wav data: %v", err)
				return
			}

			// Copy the mic buffer for echo
			frameCopy := make([]float32, len(micBuffer))
			copy(frameCopy, micBuffer)

			// Schedule echo
			go func(data []float32) {
				time.Sleep(echoDelay)
				echoChan <- data
			}(frameCopy)
		}
	}()

	fmt.Println("Press Ctrl+C to stop. You'll hear 300 ms echo in speakers, while the raw mic is recorded to recorded_mic.wav...")

	// Block forever
	select {}
}
