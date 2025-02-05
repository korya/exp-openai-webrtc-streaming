package main

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
	"unsafe"

	"github.com/gordonklaus/portaudio"
	"github.com/hraban/opus"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

// Audio constants
const (
	sampleRate       = 48000                                 // 48 kHz
	channels         = 1                                     // mono
	frameDurationMs  = 20                                    // 20 ms frames
	samplesPerFrame  = (sampleRate * frameDurationMs) / 1000 // 960 samples for 20ms @ 48kHz
	maxOpusFrameSize = 4000                                  // max bytes for an Opus frame (somewhat arbitrary)
)

func main() {
	// Initialize PortAudio
	if err := portaudio.Initialize(); err != nil {
		log.Fatalf("Failed to initialize PortAudio: %v", err)
	}
	defer portaudio.Terminate()

	// Create an encoder and decoder (for sending and receiving Opus audio).
	opusEncoder, err := opus.NewEncoder(sampleRate, channels, opus.AppAudio)
	if err != nil {
		log.Fatalf("Failed to create Opus encoder: %v", err)
	}
	opusDecoder, err := opus.NewDecoder(sampleRate, channels)
	if err != nil {
		log.Fatalf("Failed to create Opus decoder: %v", err)
	}

	// We will store incoming decoded samples in a queue for playback
	decodedSamplesQueue := make(chan []float32, 100)

	// Create a MediaEngine and register default codecs (including Opus).
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterDefaultCodecs(); err != nil {
		log.Fatalf("Failed to register default codecs: %v", err)
	}

	// Create the API object
	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine))

	// Create PeerConnection configs for "offer" and "answer" side
	config := webrtc.Configuration{}

	pcOffer, err := api.NewPeerConnection(config)
	if err != nil {
		log.Fatalf("Failed to create offer PeerConnection: %v", err)
	}
	defer pcOffer.Close()

	pcAnswer, err := api.NewPeerConnection(config)
	if err != nil {
		log.Fatalf("Failed to create answer PeerConnection: %v", err)
	}
	defer pcAnswer.Close()

	// Create a local track for outgoing Opus audio on the "offer" side.
	micTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
		"audio",
		"pion-offer",
	)
	if err != nil {
		log.Fatalf("Failed to create local track: %v", err)
	}

	// Add this track to the offer side PeerConnection
	_, err = pcOffer.AddTrack(micTrack)
	if err != nil {
		log.Fatalf("Failed to AddTrack: %v", err)
	}

	// On the "answer" side, whenever we get a remote track, decode and play it
	pcAnswer.OnTrack(func(remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		log.Printf("pcAnswer received track: %s", remoteTrack.Kind().String())
		if remoteTrack.Kind() != webrtc.RTPCodecTypeAudio {
			log.Println("Non-audio track received, ignoring.")
			return
		}

		go func() {
			for {
				// Read RTP packets
				pkt, _, readErr := remoteTrack.ReadRTP()
				if readErr != nil {
					log.Printf("Remote track ReadRTP error, track ended: %v", readErr)
					return
				}
				// The Opus-encoded data is in pkt.Payload. Let's decode it.
				decoded := make([]float32, samplesPerFrame*channels)
				n, decErr := opusDecoder.DecodeFloat32(pkt.Payload, decoded)
				if decErr != nil {
					log.Printf("Opus decode error: %v", decErr)
					continue
				}
				// 'decoded[:n]' holds the valid PCM samples
				decodedSamplesQueue <- decoded[:n]
			}
		}()
	})

	//
	// Exchange SDP between pcOffer and pcAnswer (local loop for demonstration)
	//

	// Create Offer
	offer, err := pcOffer.CreateOffer(nil)
	if err != nil {
		log.Fatalf("Failed to create offer: %v", err)
	}
	if err = pcOffer.SetLocalDescription(offer); err != nil {
		log.Fatalf("Failed to SetLocalDescription on pcOffer: %v", err)
	}

	// SetRemoteDescription on pcAnswer
	if err = pcAnswer.SetRemoteDescription(*pcOffer.LocalDescription()); err != nil {
		log.Fatalf("Failed to SetRemoteDescription on pcAnswer: %v", err)
	}

	// Create Answer
	answer, err := pcAnswer.CreateAnswer(nil)
	if err != nil {
		log.Fatalf("Failed to create answer: %v", err)
	}
	if err = pcAnswer.SetLocalDescription(answer); err != nil {
		log.Fatalf("Failed to SetLocalDescription on pcAnswer: %v", err)
	}

	// SetRemoteDescription on pcOffer
	if err = pcOffer.SetRemoteDescription(*pcAnswer.LocalDescription()); err != nil {
		log.Fatalf("Failed to SetRemoteDescription on pcOffer: %v", err)
	}

	// Wait a little for ICE gathering (in real usage, you'd use OnICECandidate callbacks)
	time.Sleep(2 * time.Second)

	//
	// Now set up PortAudio input (microphone) and output (speaker).
	//

	// We'll read microphone data into this buffer (960 samples for 20ms at 48kHz, mono)
	micBuffer := make([]float32, samplesPerFrame)

	// Open the default input stream
	inStream, err := portaudio.OpenDefaultStream(
		channels, // input channels
		0,        // output channels
		float64(sampleRate),
		samplesPerFrame,
		micBuffer,
	)
	if err != nil {
		log.Fatalf("Failed to open input stream: %v", err)
	}
	defer inStream.Close()

	// For speaker output, we use a callback approach so we can fill speaker frames from a channel.
	outStream, err := portaudio.OpenDefaultStream(
		0,        // no input
		channels, // 1 output channel
		float64(sampleRate),
		samplesPerFrame,
		func(out []float32) {
			// Attempt to read a chunk of decoded samples from the queue.
			select {
			case data := <-decodedSamplesQueue:
				// data might be smaller or bigger than 'out'.
				copyLen := len(data)
				if copyLen > len(out) {
					copyLen = len(out)
				}
				copy(out, data[:copyLen])
				// Fill the rest with zeros if data is shorter than out
				for i := copyLen; i < len(out); i++ {
					out[i] = 0
				}
			default:
				// No data available, output silence
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

	// Start streams
	if err := inStream.Start(); err != nil {
		log.Fatalf("Failed to start input stream: %v", err)
	}
	if err := outStream.Start(); err != nil {
		log.Fatalf("Failed to start output stream: %v", err)
	}

	// Goroutine to capture mic -> encode Opus -> send to track
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Println("Starting microphone capture loop...")

		for {
			// Read PCM from the microphone
			if err := inStream.Read(); err != nil {
				if errors.Is(err, portaudio.InputOverflowed) {
					// Overflow can happen; log and continue
					log.Println("PortAudio input overflow")
					continue
				}
				log.Printf("Error reading mic data: %v\n", err)
				return
			}

			// Encode the PCM to Opus
			encoded := make([]byte, maxOpusFrameSize)
			n, encErr := opusEncoder.EncodeFloat32(micBuffer, encoded)
			if encErr != nil {
				log.Printf("Opus encode error: %v\n", encErr)
				continue
			}

			// Write the encoded Opus frame to the WebRTC track
			// Duration for a 20ms frame at 48kHz is 20ms
			sampleDuration := (time.Duration(frameDurationMs) * time.Millisecond)
			writeErr := micTrack.WriteSample(media.Sample{
				Data:     encoded[:n],
				Duration: sampleDuration,
			})
			if writeErr != nil {
				log.Printf("Error writing sample to micTrack: %v\n", writeErr)
				return
			}
		}
	}()

	fmt.Println("Press Ctrl+C to stop (or kill the process). The mic audio should loop back to your speaker via Opus/WebRTC...")

	// Block forever (or until mic loop returns)
	wg.Wait()
	fmt.Println("Exiting...")
}

// Optional helper to convert float32 -> bytes if you ever need it.
// Not used in the final code below, but might be useful for debugging.
func float32ToBytes(val float32) []byte {
	bits := make([]byte, 4)
	u := *(*uint32)(unsafe.Pointer(&val))
	bits[0] = byte(u)
	bits[1] = byte(u >> 8)
	bits[2] = byte(u >> 16)
	bits[3] = byte(u >> 24)
	return bits
}
