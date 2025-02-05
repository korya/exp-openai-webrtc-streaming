package main

import (
	"fmt"
	"sync"

	"github.com/ebitengine/oto/v3"
	"github.com/pion/opus"
	"github.com/pion/webrtc/v4"
)

type OpusV3AudioPlayer struct {
	context     *oto.Context
	player      *oto.Player
	audioBuffer *audioBuffer
	mutex       sync.Mutex
	closed      bool
}

func NewOpusV3AudioPlayer() (*OpusV3AudioPlayer, error) {
	context, ready, err := oto.NewContext(&oto.NewContextOptions{
		SampleRate:   sampleRate,
		ChannelCount: channels,
		Format:       oto.FormatFloat32LE,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create audio context: %w", err)
	}

	// Wait for the context to be ready
	<-ready

	audioBuffer := newAudioBuffer(sampleRate, channels)

	player := context.NewPlayer(audioBuffer)

	return &OpusV3AudioPlayer{
		context:     context,
		player:      player,
		audioBuffer: audioBuffer,
	}, nil
}

func (ap *OpusV3AudioPlayer) WriteWebRTCTrack(track *webrtc.TrackRemote) error {
	codec := track.Codec()

	if codec.MimeType != webrtc.MimeTypeOpus {
		return fmt.Errorf("unsupported codec: %s", track.Codec().MimeType)
	}

	ap.player.Play()

	decoder := opus.NewDecoder()

	// Buffer for decoded PCM data
	pcmBuf := make([]byte, 96*20*4)

	for {
		if ap.closed {
			return nil
		}

		p, _, err := track.ReadRTP()
		if err != nil {
			return fmt.Errorf("failed to read RTP packet: %w", err)
		}

		for i := 0; i < len(pcmBuf); i++ {
			pcmBuf[i] = 0
		}

		ap.mutex.Lock()
		// Decode Opus data to PCM
		bandwidth, _, err := decoder.Decode(p.Payload, pcmBuf)
		if err != nil {
			ap.mutex.Unlock()
			fmt.Printf("Failed to decode opus data: %v\n", err)
			continue
		}

		_ = bandwidth

		if _, err := ap.audioBuffer.Write(pcmBuf); err != nil {
			ap.mutex.Unlock()
			fmt.Printf("Failed to write to audio buffer: %v\n", err)
			continue
		}

		ap.mutex.Unlock()
		if !ap.player.IsPlaying() {
			ap.player.Play()
		}
	}
}

func (ap *OpusV3AudioPlayer) Close() error {
	ap.mutex.Lock()
	defer ap.mutex.Unlock()

	if ap.closed {
		return nil
	}

	ap.closed = true
	ap.player.Close()
	ap.player = nil
	ap.context = nil
	return nil
}
