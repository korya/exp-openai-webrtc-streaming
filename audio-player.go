package main

import (
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	"github.com/ebitengine/oto/v3"
	opusv2 "github.com/hraban/opus"
	"github.com/pion/webrtc/v4"
	"golang.org/x/exp/rand"
)

const (
	sampleRate = 24_000 // 24kHz
	channels   = 2      // stereo
)

type OpusV2AudioPlayer struct {
	context     *oto.Context
	player      *oto.Player
	audioBuffer *audioBuffer
	mutex       sync.Mutex
	closed      bool
}

func NewOpusV2AudioPlayer() (*OpusV2AudioPlayer, error) {
	context, ready, err := oto.NewContext(&oto.NewContextOptions{
		SampleRate:   sampleRate,
		ChannelCount: channels,
		Format:       oto.FormatSignedInt16LE,
		BufferSize:   sampleRate / 100, // 10ms buffer (lower for less latency)
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create audio context: %w", err)
	}

	// Wait for the context to be ready
	<-ready

	audioBuffer := newAudioBuffer(sampleRate, channels)
	player := context.NewPlayer(audioBuffer)
	// Try to set real-time priority if possible
	if err := setRealtimePriority(); err != nil {
		fmt.Printf("Warning: Could not set realtime priority: %v\n", err)
	}

	return &OpusV2AudioPlayer{
		context:     context,
		player:      player,
		audioBuffer: audioBuffer,
	}, nil
}

func (ap *OpusV2AudioPlayer) orig_WriteWebRTCTrack(track *webrtc.TrackRemote) error {
	codec := track.Codec()

	if codec.MimeType != webrtc.MimeTypeOpus {
		return fmt.Errorf("unsupported codec: %s", track.Codec().MimeType)
	}

	sampleRate := int(codec.ClockRate)
	channels := int(codec.Channels)
	decoder, err := opusv2.NewDecoder(sampleRate, channels)
	if err != nil {
		return fmt.Errorf("failed to create opus decoder: %w", err)
	}

	ap.player.Play()

	// Allocate PCM buffers at maximum size
	frameSizeMs := 60 // for max frameSize
	frameSize := int(float32(frameSizeMs) * float32(sampleRate) / 1000)
	pcmBuf := make([]int16, frameSize*channels)
	bsBuf := make([]byte, len(pcmBuf)*2) // *2 for 16-bit samples

	for {
		var ts struct {
			start   time.Time
			lock    time.Time
			read    time.Time
			decode  time.Time
			convert time.Time
			write   time.Time
			end     time.Time
		}

		if ap.closed {
			return nil
		}

		ts.start = time.Now()
		p, _, err := track.ReadRTP()
		if err != nil {
			return fmt.Errorf("failed to read RTP packet: %w", err)
		}

		ts.read = time.Now()
		ap.mutex.Lock()

		ts.lock = time.Now()
		// Decode Opus data to PCM
		samplesPerChannel, err := decoder.Decode(p.Payload, pcmBuf)
		if err != nil {
			ap.mutex.Unlock()
			fmt.Printf("Failed to decode opus data: %v\n", err)
			continue
		}

		totalSamples := samplesPerChannel * 2

		ts.decode = time.Now()
		// Convert int16 PCM to bytes
		for i := 0; i < totalSamples; i++ {
			sample := pcmBuf[i]
			byteIndex := i * 2
			bsBuf[byteIndex] = byte(sample)
			bsBuf[byteIndex+1] = byte(sample >> 8)
		}

		ts.convert = time.Now()
		if _, err := ap.audioBuffer.Write(bsBuf[:totalSamples*2]); err != nil {
			ap.mutex.Unlock()
			fmt.Printf("Failed to write to audio buffer: %v\n", err)
			continue
		}

		ts.write = time.Now()
		ap.mutex.Unlock()
		if !ap.player.IsPlaying() {
			ap.player.Play()
		}

		ts.end = time.Now()

		if rand.Intn(10) == 0 {
			fmt.Printf(`RTP loop stats:
    read:  %s
    lock:  %s
  decode:  %s
 convert:  %s
   write:  %s
     end:  %s
 => TOTAL: %s
		`,
				ts.read.Sub(ts.start),
				ts.lock.Sub(ts.read),
				ts.decode.Sub(ts.lock),
				ts.convert.Sub(ts.decode),
				ts.write.Sub(ts.convert),
				ts.end.Sub(ts.write),
				ts.end.Sub(ts.start),
			)
		}
	}
}

// Modified WriteWebRTCTrack with diagnostics
func (ap *OpusV2AudioPlayer) WriteWebRTCTrack(track *webrtc.TrackRemote) error {
	codec := track.Codec()
	if codec.MimeType != webrtc.MimeTypeOpus {
		return fmt.Errorf("unsupported codec: %s", codec.MimeType)
	}

	// sampleRate := int(codec.ClockRate)
	// channels := int(codec.Channels)
	decoder, err := opusv2.NewDecoder(sampleRate, channels)
	if err != nil {
		return fmt.Errorf("failed to create opus decoder: %w", err)
	}

	// Print codec details
	fmt.Printf("Codec Details:\n")
	fmt.Printf("MimeType: %s\n", codec.MimeType)
	fmt.Printf("ClockRate: %d\n", codec.ClockRate)
	fmt.Printf("Channels: %d\n", codec.Channels)
	fmt.Printf("SDPFmtpLine: %s\n", codec.SDPFmtpLine)

	// Start playing
	ap.player.Play()

	diagnostics := NewAudioDiagnostics()

	// Allocate PCM buffers at maximum size
	frameSizeMs := 60 // for max frameSize
	frameSize := int(float32(frameSizeMs) * float32(sampleRate) / 1000)
	pcmBuf := make([]int16, frameSize*channels)
	byteBuf := make([]byte, len(pcmBuf)*2)

	var ts processLoopStats

	for {
		ts.startSample()
		if ap.closed {
			return nil
		}

		p, _, err := track.ReadRTP()
		if err != nil {
			return fmt.Errorf("failed to read RTP packet: %w", err)
		}

		// if p.Padding {
		// 	fmt.Println("PADDING")
		// }
		// if p.Extension {
		// 	fmt.Println("EXTENTSON")
		// }
		// if p.Marker {
		// 	fmt.Println("MARKER")
		// }
		// if p.PayloadOffset != 12 {
		// 	fmt.Printf("PAYLOADOFFSET: %d\n", p.PayloadOffset)
		// }

		ts.read += ts.sinceLastMeasure()
		ap.mutex.Lock()

		ts.lock += ts.sinceLastMeasure()
		// Decode Opus data to PCM
		samplesPerChannel, err := decoder.Decode(p.Payload, pcmBuf)
		if err != nil {
			ap.mutex.Unlock()
			fmt.Printf("Failed to decode opus data: %v\n", err)
			continue
		}

		ts.decode += ts.sinceLastMeasure()
		// Log diagnostics
		diagnostics.logStats(pcmBuf[:samplesPerChannel*2], p.Payload, samplesPerChannel)

		totalSamples := samplesPerChannel * 2

		// Convert int16 PCM to bytes (little-endian)
		nBytes, err := toByteArray(pcmBuf[:totalSamples], byteBuf)
		if err != nil {
			ap.mutex.Unlock()
			fmt.Printf("Failed to convert PCM to bytes: %v\n", err)
			continue
		}
		// for i := 0; i < totalSamples; i++ {
		// 	sample := pcmBuf[i]
		// 	byteIndex := i * 2
		// 	byteBuf[byteIndex] = byte(sample)
		// 	byteBuf[byteIndex+1] = byte(sample >> 8)
		// }

		ts.convert += ts.sinceLastMeasure()
		// Write to audio buffer
		if _, err := ap.audioBuffer.Write(byteBuf[:nBytes]); err != nil {
			ap.mutex.Unlock()
			fmt.Printf("Failed to write to audio buffer: %v\n", err)
			continue
		}

		ts.write += ts.sinceLastMeasure()
		ap.mutex.Unlock()

		ts.unlock += ts.sinceLastMeasure()
		if !ap.player.IsPlaying() {
			ap.player.Play()
		}

		ts.end += ts.sinceLastMeasure()
		ts.endSample()
	}
}

func (ap *OpusV2AudioPlayer) Close() error {
	ap.mutex.Lock()
	defer ap.mutex.Unlock()

	if ap.closed {
		return nil
	}

	ap.closed = true
	if ap.player != nil {
		ap.player.Close()
	}
	return nil
}

// Helper function to convert float32 PCM data to bytes
func float32ToBytes(samples []float32) []byte {
	bytes := make([]byte, len(samples)*4)
	for i, sample := range samples {
		// Clamp the sample to [-1, 1]
		if sample > 1 {
			sample = 1
		} else if sample < -1 {
			sample = -1
		}

		// Convert to int32 and then to bytes
		intSample := int32(sample * 2147483647) // Scale to full int32 range
		bytes[i*4] = byte(intSample)
		bytes[i*4+1] = byte(intSample >> 8)
		bytes[i*4+2] = byte(intSample >> 16)
		bytes[i*4+3] = byte(intSample >> 24)
	}
	return bytes
}

// Platform-specific code for setting realtime priority
func setRealtimePriority() error {
	// This is just a placeholder - implement based on your OS
	return nil
}

func toByteArray(buf []int16, bytes []byte) (int, error) {
	if len(buf)*2 > len(bytes) {
		return 0, fmt.Errorf("invalid buffer sizes: buf=%d bytes=%d", len(buf), len(bytes))
	}

	bi := 0
	for i := 0; i < len(buf); i++ {
		binary.LittleEndian.PutUint16(bytes[bi:], uint16(buf[i]))
		bi += 2
	}
	return bi, nil
}

type processLoopStats struct {
	read     time.Duration
	lock     time.Duration
	decode   time.Duration
	convert  time.Duration
	write    time.Duration
	unlock   time.Duration
	end      time.Duration
	total    time.Duration
	nsamples int

	startedAt     time.Time
	lastTimePoint time.Time
}

func (s *processLoopStats) startSample() {
	s.startedAt = time.Now()
	s.lastTimePoint = s.startedAt
}

func (s *processLoopStats) sinceLastMeasure() time.Duration {
	now := time.Now()
	elapsed := now.Sub(s.lastTimePoint)
	s.lastTimePoint = now
	return elapsed
}

func (s *processLoopStats) endSample() {
	s.total += time.Since(s.startedAt)
	s.nsamples++
	if s.nsamples%100 == 0 {
		s.Print()
	}
}

func (s *processLoopStats) Print() {
	n := time.Duration(s.nsamples)

	fmt.Printf(`RTP loop stats:
    read:  %s
    lock:  %s
  decode:  %s
 convert:  %s
   write:  %s
  unlock:  %s
     end:  %s
 => TOTAL: %s
		`,
		s.read/n,
		s.lock/n,
		s.decode/n,
		s.convert/n,
		s.write/n,
		s.unlock/n,
		s.end/n,
		s.total/n,
	)
}
