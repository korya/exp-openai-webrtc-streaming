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

// We still use these constants to define sampleRate=48k, 2 channels, etc.,
// but note that for input, we actually only have 1 channel available (mono).
const (
	sampleRate      = 48000
	outputChannels  = 2 // For WAV/output
	frameDurationMs = 20
	framesPerBuffer = (sampleRate * frameDurationMs) / 1000 // e.g., 960
)

const echoDelay = 300 * time.Millisecond

func main() {
	// 1) Initialize PortAudio
	if err := portaudio.Initialize(); err != nil {
		log.Fatalf("Failed to initialize PortAudio: %v", err)
	}
	// We’ll explicitly terminate in the signal handler.

	// 2) Create/prepare the WAV file (2-channel)
	wavFile, err := os.Create("recorded_mic.wav")
	if err != nil {
		log.Fatalf("Failed to create wav file: %v", err)
	}

	enc := wav.NewEncoder(
		wavFile,
		sampleRate,
		16, // 16-bit
		outputChannels,
		1, // WAV type (1 = PCM)
	)

	// 3) Create the buffers
	//    - micBufferMono for 1-channel input
	//    - stereoBuffer to up-mix the mono samples to 2 channels
	micBufferMono := make([]float32, framesPerBuffer)               // mono
	stereoBuffer := make([]float32, framesPerBuffer*outputChannels) // 2x size

	// This channel is how we pass frames to the speaker’s output callback
	echoChan := make(chan []float32, 100)

	// 4) Open a *mono* input stream (1 channel)
	inStream, err := portaudio.OpenDefaultStream(
		1, // mic: 1 channel
		0, // no output
		float64(sampleRate),
		framesPerBuffer,
		micBufferMono, // read data into this mono buffer
	)
	if err != nil {
		log.Fatalf("Failed to open input stream: %v", err)
	}

	// 5) Open a *stereo* output stream (2 channels) for echo
	outStream, err := portaudio.OpenDefaultStream(
		0,              // no input
		outputChannels, // 2 output channels
		float64(sampleRate),
		framesPerBuffer,
		func(out []float32) {
			// The output callback: copy from echoChan (if available) into out
			select {
			case delayedFrame := <-echoChan:
				copy(out, delayedFrame)
				// zero any leftover (usually none, but just in case)
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

	// Start input/output streams
	if err := inStream.Start(); err != nil {
		log.Fatalf("Failed to start input stream: %v", err)
	}
	if err := outStream.Start(); err != nil {
		log.Fatalf("Failed to start output stream: %v", err)
	}

	// 6) Graceful shutdown on Ctrl+C
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("Caught Ctrl+C or SIGTERM, closing streams and WAV file gracefully...")

		// Stop/Close streams
		inStream.Stop()
		outStream.Stop()
		inStream.Close()
		outStream.Close()

		// Close WAV encoder and file
		enc.Close()
		wavFile.Close()

		// Terminate PortAudio
		portaudio.Terminate()
		os.Exit(0)
	}()

	// 7) Goroutine: read mic frames, up-mix to stereo, write WAV, schedule echo
	go func() {
		log.Println("Starting microphone capture + WAV record loop...")

		for {
			// Read a mono frame
			if err := inStream.Read(); err != nil {
				if errors.Is(err, portaudio.InputOverflowed) {
					log.Println("PortAudio input overflowed, ignoring")
					continue
				}
				log.Printf("Error reading mic data: %v\n", err)
				return
			}

			// Up-mix from mono => stereoBuffer
			//   micBufferMono[i] -> stereoBuffer[2*i], stereoBuffer[2*i+1]
			for i := 0; i < framesPerBuffer; i++ {
				s := micBufferMono[i]
				stereoBuffer[2*i] = s   // left
				stereoBuffer[2*i+1] = s // right
			}

			//
			// Write the stereoBuffer to our WAV
			//
			// Convert float32 -> 16-bit int range
			intBuf := make([]int, len(stereoBuffer))
			for i, sample := range stereoBuffer {
				v := int(sample * 32767)
				if v < -32768 {
					v = -32768
				} else if v > 32767 {
					v = 32767
				}
				intBuf[i] = v
			}

			// Make an AudioBuffer for go-audio/wav
			audioBuf := &audio.IntBuffer{
				Format:         &audio.Format{NumChannels: outputChannels, SampleRate: sampleRate},
				SourceBitDepth: 16,
				Data:           intBuf,
			}

			if err := enc.Write(audioBuf); err != nil {
				log.Printf("Error writing WAV data: %v", err)
				return
			}

			//
			// Schedule a 300 ms echo
			//
			frameCopy := make([]float32, len(stereoBuffer))
			copy(frameCopy, stereoBuffer)

			go func(data []float32) {
				time.Sleep(echoDelay)
				echoChan <- data
			}(frameCopy)
		}
	}()

	fmt.Println("Press Ctrl+C to stop. You'll hear a 300 ms stereo echo, while the raw mic is recorded to 'recorded_mic.wav'...")
	select {}
}
