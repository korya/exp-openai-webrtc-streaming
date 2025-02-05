package main

import (
	"fmt"
	"log"
	"os"

	"github.com/pion/webrtc/v4"
)

func main() {
	// old_main()
	testMicrophoneRecording()

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Fatalln("OPENAI_API_KEY is not set")
		return
	}

	c := NewOpenAIRealtimeAPI(apiKey)
	defer c.Disconnect()

	// player, err := getAudioPlayer("portaudio")
	player, err := getAudioPlayer("oto-v2")
	// player, err := getAudioPlayer("oto-v3")
	if err != nil {
		log.Fatalf("Failed to create audio player: %v\n", err)
	}
	defer player.Close()

	userMediaTrack, err := getUserMediaTrack(sampleRate, channels)
	if err != nil {
		log.Fatalf("Failed to get user media tracks: %v\n", err)
	}

	if err := c.Connect(userMediaTrack, player); err != nil {
		log.Fatalf("Failed to connect to OpenAI Realtime API: %v\n", err)
	}

	log.Println("Connected to OpenAI Realtime API")
	c.dataChannel.SendText("Hello, OpenAI Realtime API!")

	// Keep the program running
	select {}
}

type AudioPlayer interface {
	WriteWebRTCTrack(track *webrtc.TrackRemote) error
	Close() error
}

func getAudioPlayer(name string) (AudioPlayer, error) {
	switch name {
	case "oto-v2":
		return NewOpusV2AudioPlayer()
	case "oto-v3":
		return NewOpusV3AudioPlayer()
	case "portaudio":
		return NewPortaudioPlayer()
	default:
		return nil, fmt.Errorf("unknown audio player: %s", name)
	}
}
