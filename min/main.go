package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gordonklaus/portaudio"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

// Replace with actual OpenAI Realtime WebRTC endpoint if documented.
const openAIRealtimeEndpoint = "https://api.openai.com/v1/realtime/webrtc"

// Optional flags for troubleshooting
var (
	flagDebug     = flag.Bool("debug", false, "print debug logs")
	flagRecordMic = flag.Bool("record-mic", false, "record microphone output to file")
	flagRecordRtc = flag.Bool("record-rtc", false, "record incoming RTC audio to file")
)

// globalStop signals a stop to all goroutines
var globalStop = make(chan struct{})

// AudioWriter is a utility to record audio samples to a file for troubleshooting.
type AudioWriter struct {
	file *os.File
}

func NewAudioWriter(filename string) (*AudioWriter, error) {
	f, err := os.Create(filename)
	if err != nil {
		return nil, err
	}
	// For simplicity, we’ll just dump raw PCM or you could write WAV headers.
	return &AudioWriter{file: f}, nil
}

func (aw *AudioWriter) WriteSamples(samples []int16) {
	// Convert int16 to []byte and write
	buf := make([]byte, len(samples)*2)
	for i, s := range samples {
		buf[2*i] = byte(s)
		buf[2*i+1] = byte(s >> 8)
	}
	aw.file.Write(buf)
}

func (aw *AudioWriter) Close() {
	if aw.file != nil {
		_ = aw.file.Close()
	}
}

// OpenAIRealtimeOffer is a placeholder for how the server might return an offer.
// You’d adapt this to your actual API call or session creation flow.
type OpenAIRealtimeOffer struct {
	SDP  string `json:"sdp"`
	Type string `json:"type"` // usually "offer"
}

// OpenAIRealtimeAnswer is a placeholder for how you might post the local answer back to the server.
type OpenAIRealtimeAnswer struct {
	SDP  string `json:"sdp"`
	Type string `json:"type"` // usually "answer"
}

func main() {
	flag.Parse()

	// 1) Initialize PortAudio (for mic + speaker).
	if err := portaudio.Initialize(); err != nil {
		log.Fatalf("failed to initialize portaudio: %v", err)
	}
	defer portaudio.Terminate()

	// Handle interrupts so we can gracefully cleanup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go handleInterrupt(cancel)

	// 2) Retrieve Offer from OpenAI (placeholder: this might be an HTTP POST to create a session).
	offer, err := getOpenAIRealtimeOffer()
	if err != nil {
		log.Fatalf("Error getting offer from OpenAI: %v", err)
	}
	if *flagDebug {
		log.Printf("Received remote offer from OpenAI: type=%s, sdp=%s", offer.Type, offer.SDP)
	}

	// 3) Create PeerConnection
	peerConnection, err := webrtc.NewPeerConnection(webrtc.Configuration{
		// Depending on your environment you may need TURN servers:
		// ICEServers: []webrtc.ICEServer{
		//   {
		//       URLs:       []string{"stun:stun1.l.google.com:19302"},
		//   },
		// },
	})
	if err != nil {
		log.Fatalf("Error creating PeerConnection: %v", err)
	}
	defer peerConnection.Close()

	// 4) Create a local audio track for the microphone to send to OpenAI
	audioTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{
			MimeType: "audio/opus",
		},
		"audio-track",
		"audio-publisher",
	)
	if err != nil {
		log.Fatalf("Error creating local audio track: %v", err)
	}

	_, err = peerConnection.AddTrack(audioTrack)
	if err != nil {
		log.Fatalf("Error adding track to PeerConnection: %v", err)
	}

	// 5) Set a handler for incoming tracks (i.e., the audio from OpenAI).
	var speakerWriter *portaudio.Stream
	var rtcAudioWriter *AudioWriter
	peerConnection.OnTrack(func(remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		if *flagDebug {
			log.Printf("OnTrack fired: kind=%s, codec=%s", remoteTrack.Kind(), remoteTrack.Codec().MimeType)
		}

		// We only handle audio track in this example
		if remoteTrack.Kind() == webrtc.RTPCodecTypeAudio {
			// Initialize speaker output for playback
			var errSpeaker error
			speakerWriter, errSpeaker = createSpeakerStream()
			if errSpeaker != nil {
				log.Printf("Error creating speaker stream: %v", errSpeaker)
				return
			}
			if err := speakerWriter.Start(); err != nil {
				log.Printf("Error starting speaker stream: %v", err)
				return
			}

			// Optional file writer for remote audio
			if *flagRecordRtc {
				rtcAudioWriter, _ = NewAudioWriter("remote_webrtc_audio.raw")
			}

			// Read incoming RTCP or RTP packets from remote track
			go receiveRemoteAudio(ctx, remoteTrack, speakerWriter, rtcAudioWriter)
		}
	})

	// 6) Set remote description from the OpenAI-provided SDP offer
	if err := peerConnection.SetRemoteDescription(webrtc.SessionDescription{
		SDP:  offer.SDP,
		Type: webrtc.SDPTypeOffer,
	}); err != nil {
		log.Fatalf("Error setting remote description: %v", err)
	}

	// 7) Create our local answer
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		log.Fatalf("Error creating answer: %v", err)
	}

	// 8) Gather ICE candidates and finalize the SDP
	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)
	if err := peerConnection.SetLocalDescription(answer); err != nil {
		log.Fatalf("Error setting local description: %v", err)
	}
	<-gatherComplete

	localAnswer := peerConnection.LocalDescription()
	if *flagDebug {
		log.Printf("Local Answer: %s", localAnswer.SDP)
	}

	// 9) Send the local answer to OpenAI so they can set their remote description
	if err := sendAnswerToOpenAI(localAnswer); err != nil {
		log.Printf("Warning: failed to send answer to OpenAI, voice may fail: %v", err)
	}

	// 10) Now we can start capturing mic audio and sending it out the local track.
	var micWriter *AudioWriter
	if *flagRecordMic {
		micWriter, _ = NewAudioWriter("mic_audio.raw")
	}
	micStream, err := createMicStream(ctx, audioTrack, micWriter)
	if err != nil {
		log.Fatalf("Error creating mic stream: %v", err)
	}
	if err := micStream.Start(); err != nil {
		log.Fatalf("Error starting mic stream: %v", err)
	}

	// Optional: watch for ICE connection state changes
	peerConnection.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		if *flagDebug {
			log.Printf("ICE Connection State has changed to %s", state.String())
		}
	})

	// Block until ctrl+c or error
	<-ctx.Done()

	if micWriter != nil {
		micWriter.Close()
	}
	if rtcAudioWriter != nil {
		rtcAudioWriter.Close()
	}
	if speakerWriter != nil {
		_ = speakerWriter.Stop()
		_ = speakerWriter.Close()
	}
	_ = micStream.Stop()
	_ = micStream.Close()
	log.Println("Shutting down gracefully.")
}

// getOpenAIRealtimeOffer simulates retrieving an SDP offer from OpenAI’s Realtime WebRTC API.
func getOpenAIRealtimeOffer() (*OpenAIRealtimeOffer, error) {
	// In practice, you’d do an HTTP request:
	//   POST https://api.openai.com/v1/realtime/webrtc/session
	//   Headers: Authorization: Bearer YOUR_OPENAI_API_KEY
	//   The response might contain the offer SDP.
	//
	// This is a placeholder. In reality you’d parse the JSON body from the response.
	return &OpenAIRealtimeOffer{
		SDP:  "v=0\r\n[...]A_LONG_SDP_OFFER_FROM_OPENAI[...]\r\n",
		Type: "offer",
	}, nil
}

// sendAnswerToOpenAI simulates posting our local SDP answer to OpenAI.
func sendAnswerToOpenAI(desc *webrtc.SessionDescription) error {
	answerPayload := OpenAIRealtimeAnswer{
		SDP:  desc.SDP,
		Type: "answer",
	}

	// Example of JSON encoding
	body, err := json.Marshal(answerPayload)
	if err != nil {
		return err
	}

	// In practice, send to OpenAI:
	//   PUT or POST https://api.openai.com/v1/realtime/webrtc/session/<session_id>/answer
	//   with Authorization header, etc.

	if *flagDebug {
		log.Printf("Sending local answer to OpenAI: %s", string(body))
	}

	// pretend success
	return nil
}

// createMicStream sets up a PortAudio input stream capturing from the microphone and
// relaying data to the WebRTC audioTrack. Optionally writes samples to a file.
func createMicStream(ctx context.Context, audioTrack *webrtc.TrackLocalStaticSample, aw *AudioWriter) (*portaudio.Stream, error) {
	in := make([]int16, 480) // Buffer of 480 samples -> 10ms of audio @ 48kHz

	// We'll capture at 48kHz, mono. This matches typical Opus channel layout.
	micStream, err := portaudio.OpenDefaultStream(
		1,       // 1 input channel (mono)
		0,       // 0 output channels
		48000,   // sample rate
		len(in), // frames per buffer
		func(inBuffer []int16) {
			// Optionally write to a local file
			if aw != nil {
				aw.WriteSamples(inBuffer)
			}
			// Encode as a sample chunk for WebRTC
			err := audioTrack.WriteSample(
				media.Sample{
					Data:     int16ToLittleEndianBytes(inBuffer),
					Duration: time.Duration(len(inBuffer)) * time.Second / 48000,
				},
			)
			if err != nil && *flagDebug {
				log.Printf("Error writing mic samples to track: %v", err)
			}
		},
	)
	if err != nil {
		return nil, err
	}

	// The stream will run in the background, so we just need to stop it when ctx is canceled.
	go func() {
		<-ctx.Done()
		_ = micStream.Stop()
		_ = micStream.Close()
	}()

	return micStream, nil
}

// createSpeakerStream sets up a PortAudio output stream for playing audio.
func createSpeakerStream() (*portaudio.Stream, error) {
	out := make([]int16, 480) // 10ms of audio at 48kHz
	// We do not fill 'out' here directly; we’ll fill it in a callback from remote track.

	return portaudio.OpenDefaultStream(
		0,     // 0 input channels
		1,     // 1 output channel (mono)
		48000, // sample rate
		len(out),
		func(outBuffer []int16) {
			// We’ll manually fill outBuffer in the track reader code.
			// This callback is called repeatedly by PortAudio to pull data.
			// For a simple approach, do nothing; we’ll copy data later.
		},
	)
}

// receiveRemoteAudio continuously reads from the remote track and writes to speaker/rtc file.
func receiveRemoteAudio(ctx context.Context,
	remoteTrack *webrtc.TrackRemote,
	speakerStream *portaudio.Stream,
	rtcWriter *AudioWriter,
) {
	// Pion provides a method to read sample packets directly.
	// We’ll use Read() in a loop.
	speakerBuffers := make(chan []int16, 30) // hold up to 30 frames in queue
	var wg sync.WaitGroup

	// 1. Goroutine to convert RTP->PCM
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			// Read RTP packet as sample
			pkt, _, err := remoteTrack.ReadRTP()
			if err != nil {
				log.Printf("Remote track read error: %v", err)
				return
			}
			// You’d decode the Opus packet here into raw PCM if necessary.
			// For brevity, assume we already have PCM or that Pion handles that.
			// In reality, you’ll need to handle Opus -> PCM decoding (check `webrtc.OpusReadCloser` in Pion).

			// For a real implementation, you'd do something like:
			//   opusDecoder.Decode(pkt.Payload, ... ) => PCM
			// Here we just pretend it's PCM int16.
			pcmData := fakeOpusDecode(pkt.Payload)

			if rtcWriter != nil {
				rtcWriter.WriteSamples(pcmData)
			}

			// Send for speaker playback
			speakerBuffers <- pcmData
		}
	}()

	// 2. Goroutine to pass PCM data to speaker
	wg.Add(1)
	go func() {
		defer wg.Done()

		outBuffer := make([]int16, 480)
		for {
			select {
			case <-ctx.Done():
				return
			case pcmData := <-speakerBuffers:
				// We must push this data to the speaker’s output stream.
				// But PortAudio uses a callback. We can fill an internal buffer that
				// the callback reads from. Or we can use the "Write" interface in
				// blocking mode. One approach is to close the stream's callback and
				// directly write samples:
				copy(outBuffer, pcmData)
				if err := speakerStream.Write(); err != nil {
					log.Printf("speakerStream.Write error: %v", err)
				}
				// Then in a real scenario, you'd have a buffer the callback uses to read outBuffer
				// This is a simplified approach; it depends on your PortAudio usage pattern.
			}
		}
	}()

	// Wait until context done
	<-ctx.Done()
	close(speakerBuffers)
	wg.Wait()
}

// fakeOpusDecode is a placeholder that “decodes” an Opus packet to PCM samples.
func fakeOpusDecode(opusPayload []byte) []int16 {
	// In real usage, you'd decode Opus frames with an Opus decoder library.
	// The length depends on the packet’s frames. This is just a stub that
	// returns a buffer of 480 samples (10ms).
	return make([]int16, 480)
}

// Convert int16 slice to little-endian bytes
func int16ToLittleEndianBytes(samples []int16) []byte {
	data := make([]byte, len(samples)*2)
	for i, s := range samples {
		data[2*i] = byte(s)
		data[2*i+1] = byte(s >> 8)
	}
	return data
}

// handleInterrupt listens for Ctrl+C / SIGTERM to gracefully shut down.
func handleInterrupt(cancelFunc context.CancelFunc) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c
	cancelFunc()
	signal.Stop(c)
}
