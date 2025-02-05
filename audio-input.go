package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/go-audio/audio"
	"github.com/go-audio/wav"
	opusv2 "github.com/hraban/opus"
	"github.com/pion/mediadevices"
	"github.com/pion/mediadevices/pkg/codec/opus"
	_ "github.com/pion/mediadevices/pkg/driver/microphone" // 导入麦克风驱动
	"github.com/pion/mediadevices/pkg/prop"
	"github.com/pion/webrtc/v4"
	// "github.com/xiph/ogg"
)

func getUserMediaTrack(sampleRate, channels int) (mediadevices.Track, error) {
	opusParams := opus.Params{
		Latency: opus.Latency20ms,
	}

	codecSelector := mediadevices.NewCodecSelector(
		mediadevices.WithAudioEncoders(&opusParams),
	)

	audio, err := mediadevices.GetUserMedia(mediadevices.MediaStreamConstraints{
		Audio: func(c *mediadevices.MediaTrackConstraints) {
			c.SampleRate = prop.Int(sampleRate)
			c.ChannelCount = prop.Int(channels)
			c.SampleSize = prop.Int(16) // 16-bit
		},
		Codec: codecSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("Failed to get user media: %v", err)
	}

	audioTracks := audio.GetTracks()
	if len(audioTracks) == 0 {
		return nil, fmt.Errorf("no audio track found")
	}

	if len(audioTracks) > 1 {
		return nil, fmt.Errorf("too many audio tracks: %d", len(audioTracks))
	}

	return audioTracks[0], nil
}

// recordAudioToWav records audio from a mediadevices.Track into a WAV file
// for up to 'duration' or until the track ends.
//   - track:      An audio track (Track.Kind() == webrtc.RTPCodecTypeAudio).
//   - outputPath: File path for the resulting .wav file.
//   - duration:   How long to capture before stopping.
func recordAudioToWav(
	track mediadevices.Track,
	sampleRate int,
	channels int,
	duration time.Duration,
	outputPath string,
) error {
	// 1) Ensure this is an audio track
	if track.Kind() != webrtc.RTPCodecTypeAudio {
		return fmt.Errorf("track is not audio (kind=%s)", track.Kind().String())
	}

	// 2) Open the output file
	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create file '%s': %w", outputPath, err)
	}
	defer outFile.Close()

	// 3) Create a WAV encoder (16-bit PCM)
	wavEnc := wav.NewEncoder(
		outFile,
		sampleRate,
		16, // bit depth
		channels,
		1, // WAV format (1 = PCM)
	)
	defer wavEnc.Close()

	// 4) Get an EncodedReadCloser for the chosen codec (e.g. "opus")
	const codecName = "opus"
	encodedReader, err := track.NewEncodedReader(codecName)
	if err != nil {
		return fmt.Errorf("failed to create encoded reader for codec '%s': %w", codecName, err)
	}
	defer encodedReader.Close()

	// 5) If using Opus, prepare an opus.Decoder
	opusDec, err := opusv2.NewDecoder(sampleRate, 1)
	if err != nil {
		return fmt.Errorf("failed to create opus decoder: %w", err)
	}

	// A buffer to hold decoded float32 samples
	const maxOpusFrameSize = 8192
	decodedFloatBuf := make([]float32, maxOpusFrameSize)

	// We'll stop after 'duration' or when EOF occurs
	startTime := time.Now()

	fmt.Println("Recording for 5 seconds...")
	for {
		// 5a) Check if we've recorded enough
		if duration > 0 && time.Since(startTime) >= duration {
			break
		}

		// 5b) Read an EncodedBuffer from encodedReader
		encBuf, release, err := encodedReader.Read()
		if err != nil {
			if err == io.EOF {
				// Track ended
				break
			}
			return fmt.Errorf("error reading encoded data: %w", err)
		}

		fmt.Printf("=== data=%d samples=%d\n",
			len(encBuf.Data), encBuf.Samples)
		encodedFrame := encBuf.Data
		if len(encodedFrame) == 0 {
			// No data, continue
			continue
		}

		// 5c) Decode if opus, or handle raw PCM if you have a different setup
		numDecodedSamples, err := opusDec.DecodeFloat32(encodedFrame, decodedFloatBuf)
		if err != nil {
			// Decoding might fail on partial frames; you can continue or return.
			fmt.Printf("Opus decode error: %v\n", err)
			continue
		}

		// Always release once we're done with encBuf
		// (We can defer release(), but that might accumulate if we loop quickly.)
		release()

		fmt.Printf("=== decoded=%d\n", numDecodedSamples)

		if numDecodedSamples <= 0 {
			continue
		}

		// 5d) Convert float32 -> int16
		pcm16 := make([]int16, numDecodedSamples)
		for i := 0; i < numDecodedSamples; i++ {
			val := int32(decodedFloatBuf[i] * 32767)
			if val > 32767 {
				pcm16[i] = 32767
			} else if val < -32768 {
				pcm16[i] = -32768
			} else {
				pcm16[i] = int16(val)
			}
		}

		// 5e) Prepare go-audio's IntBuffer for WAV writing
		intData := make([]int, numDecodedSamples)
		for i, s := range pcm16 {
			intData[i] = int(s)
		}
		intBuf := &audio.IntBuffer{
			Format:         &audio.Format{NumChannels: channels, SampleRate: sampleRate},
			SourceBitDepth: 16,
			Data:           intData,
		}

		// 5f) Write to the WAV encoder
		if err := wavEnc.Write(intBuf); err != nil {
			return fmt.Errorf("failed to write PCM to WAV: %w", err)
		}
	}

	fmt.Printf("Wrote ~%v of audio to %s (codec=%s)\n", time.Since(startTime), outputPath, codecName)
	return nil
}

// Example usage
func testMicrophoneRecording() error {
	const (
		sampleRate  = 48_000
		inChannels  = 2
		outChannels = 2
	)

	// Get microphone track
	track, err := getUserMediaTrack(sampleRate, inChannels)
	if err != nil {
		return fmt.Errorf("failed to get media track: %w", err)
	}
	defer track.Close()

	if err := recordAudioToWav(
		track,
		sampleRate,
		outChannels,
		5*time.Second,
		"test_recording.wav",
	); err != nil {
		return fmt.Errorf("failed to record: %w", err)
	}
	fmt.Println("Recording saved to test_recording.wav")

	return nil
}
