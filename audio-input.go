package main

import (
	"fmt"

	"github.com/pion/mediadevices"
	"github.com/pion/mediadevices/pkg/codec/opus"
	_ "github.com/pion/mediadevices/pkg/driver/microphone" // 导入麦克风驱动
	"github.com/pion/mediadevices/pkg/prop"
)

func getUserMediaTrack() (mediadevices.Track, error) {
	opusParams := opus.Params{
		Latency: opus.Latency20ms,
	}

	codecSelector := mediadevices.NewCodecSelector(
		mediadevices.WithAudioEncoders(&opusParams),
	)

	audio, err := mediadevices.GetUserMedia(mediadevices.MediaStreamConstraints{
		Audio: func(c *mediadevices.MediaTrackConstraints) {
			c.SampleRate = prop.Int(48_000)
			c.ChannelCount = prop.Int(2)
			c.SampleSize = prop.Int(16)
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
