package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/pion/mediadevices"
	"github.com/pion/mediadevices/pkg/codec"
	"github.com/pion/webrtc/v4"
)

type OpenAIRealtimeAPI struct {
	Key   string
	Model string
	Voice string

	connectMutex   sync.Mutex
	ephemeralToken string
	// peerConnection is used to exchange audio over media streams
	peerConnection *webrtc.PeerConnection
	// dataChannel is used to control the "conversation"
	// https://platform.openai.com/docs/api-reference/realtime-client-events.
	dataChannel *webrtc.DataChannel
}

func NewOpenAIRealtimeAPI(key string) *OpenAIRealtimeAPI {
	return &OpenAIRealtimeAPI{
		Key:   key,
		Model: "gpt-4o-realtime-preview-2024-12-17",
		Voice: "verse",
	}
}

type WebRTCAudioWriter interface {
	WriteWebRTCTrack(track *webrtc.TrackRemote) error
}

func (c *OpenAIRealtimeAPI) Connect(
	userMediaTrack mediadevices.Track,
	audioWriter WebRTCAudioWriter,
) error {
	c.connectMutex.Lock()
	defer c.connectMutex.Unlock()

	if c.ephemeralToken == "" {
		ephemeralToken, err := c.createEphemeralToken()
		if err != nil {
			return err
		}

		c.ephemeralToken = ephemeralToken
		fmt.Printf("Created ephemeral token: %s\n", c.ephemeralToken)
	}

	if err := c.setupPeerConnection(userMediaTrack, audioWriter); err != nil {
		return err
	}

	if err := c.connectToRealtimeAPI(); err != nil {
		// Clean up on error
		c.peerConnection.Close()
		c.peerConnection = nil
		return err
	}

	if err := c.setupDataChannel(); err != nil {
		// Clean up on error
		c.peerConnection.Close()
		c.peerConnection = nil
		return err
	}

	return nil
}

func (c *OpenAIRealtimeAPI) Disconnect() {
	c.connectMutex.Lock()
	defer c.connectMutex.Unlock()

	// c.ephemeralToken = "" // no need to reset
	if c.dataChannel != nil {
		c.dataChannel.Close()
		c.dataChannel = nil
	}
	if c.peerConnection != nil {
		c.peerConnection.Close()
		c.peerConnection = nil
	}
}

func (c *OpenAIRealtimeAPI) setupPeerConnection(
	userMediaTrack mediadevices.Track,
	audioWriter WebRTCAudioWriter,
) error {
	// Create WebRTC configuration
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	}

	// XXX explicitly ask for Opus to match the Ontrack callback
	var mediaEngine webrtc.MediaEngine
	mediaEngine.RegisterCodec(codec.NewRTPOpusCodec(48_000).RTPCodecParameters, webrtc.RTPCodecTypeAudio)
	// mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
	// 	RTPCodecCapability: RTPCodecCapability{MimeTypeOpus, 48000, 2, "minptime=10;useinbandfec=1", nil},
	// 	PayloadType:        111,
	// }, webrtc.RTPCodecTypeAudio)
	api := webrtc.NewAPI(webrtc.WithMediaEngine(&mediaEngine))

	pc, err := api.NewPeerConnection(config)
	// pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		return fmt.Errorf("failed to create peer connection: %w", err)
	}

	if _, err := pc.AddTransceiverFromTrack(
		userMediaTrack,
		// webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendrecv},
	); err != nil {
		return fmt.Errorf("failed to add user media track: %w", err)
	}

	// Allow us to receive 1 audio track
	// if _, err = pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio); err != nil {
	// 	return fmt.Errorf("failed to add transceiver: %w", err)
	// }

	pc.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		fmt.Printf("+++ [pc] Received remote track:\n    streamID=%s, trackID=%s, kind=%s\n",
			track.StreamID(), track.ID(), track.Kind())
		codec := track.Codec()
		fmt.Printf("Track PayloadType: %d\n", track.PayloadType)
		fmt.Printf("Codec MimeType   : %v\n", codec.MimeType)
		fmt.Printf("Codec ClockRate  : %v\n", codec.ClockRate)
		fmt.Printf("Codec Channels   : %v\n", codec.Channels)
		fmt.Printf("Codec SDPFmtpLine: %v\n", codec.SDPFmtpLine)

		// XXX use goroutine?
		// go handleOpusTrack(track, audioPlayer)
		go func() {
			if err := audioWriter.WriteWebRTCTrack(track); err != nil {
				fmt.Printf("Failed to write WebRTC track: %v\n", err)
			}
		}()
	})

	// XXX needed?
	pc.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		fmt.Printf("+++ [pc] Connection State has changed %s\n", connectionState.String())
		// if connectionState == webrtc.ICEConnectionStateFailed {
		// 	peerConnection.Close()
		// }
	})

	pc.OnSignalingStateChange(func(sigState webrtc.SignalingState) {
		fmt.Printf("+++ [pc] Signaling State has changed %s\n", sigState.String())
	})

	c.peerConnection = pc
	return nil
}

func (c *OpenAIRealtimeAPI) connectToRealtimeAPI() error {
	// Create an offer
	offer, err := c.peerConnection.CreateOffer(nil)
	if err != nil {
		return fmt.Errorf("failed to create offer: %w", err)
	}

	// Create channel for signaling
	// gatherComplete := webrtc.GatheringCompletePromise(c.peerConnection)

	// Set local description
	err = c.peerConnection.SetLocalDescription(offer)
	if err != nil {
		return fmt.Errorf("failed to set local description: %w", err)
	}

	// Wait for ICE gathering to complete
	// Wait for gathering to complete
	// select {
	// case <-gatherComplete:
	// 	// Gathering is complete, can now send the offer
	// 	// peerConnection.LocalDescription() now contains all candidates
	// case <-time.After(time.Second * 15):
	// 	// Handle timeout
	// }

	// Send offer to OpenAI API and get answer
	answer, err := c.sendOffer(offer.SDP, c.ephemeralToken)
	if err != nil {
		return fmt.Errorf("failed to send offer: %w", err)
	}

	// Set remote description
	err = c.peerConnection.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  answer,
	})
	if err != nil {
		return fmt.Errorf("failed to set remote description: %w", err)
	}

	return nil
}

func (c *OpenAIRealtimeAPI) setupDataChannel() error {
	dc, err := c.peerConnection.CreateDataChannel("oai-events", nil)
	if err != nil {
		return fmt.Errorf("failed to create data channel: %w", err)
	}

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		fmt.Printf("+++ [dc] Received message: %s\n", string(msg.Data))
	})

	c.dataChannel = dc
	return nil
}

// getEphemeralToken creates a new ephemeral token for the OpenAI Realtime API.
//
// More details are at https://platform.openai.com/docs/api-reference/realtime-sessions/create.
func (c *OpenAIRealtimeAPI) createEphemeralToken() (string, error) {
	var bodyBuf bytes.Buffer
	if err := json.NewEncoder(&bodyBuf).Encode(map[string]interface{}{
		"model": c.Model,
		"voice": c.Voice,
	}); err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/realtime/sessions", &bodyBuf)
	if err != nil {
		return "", err
	}

	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Content-Type", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	if res.StatusCode != 200 {
		bs, _ := io.ReadAll(res.Body)
		return "", fmt.Errorf("HTTP %d\n\n%s", res.StatusCode, string(bs))
	}

	var output struct {
		ClientSecret struct {
			Value string `json:"value"`
		} `json:"client_secret"`
	}
	if err := json.NewDecoder(res.Body).Decode(&output); err != nil {
		return "", err
	}

	// should never happen
	if output.ClientSecret.Value == "" {
		return "", fmt.Errorf("got back empty token")
	}

	return output.ClientSecret.Value, nil
}

// getEphemeralToken creates a new ephemeral token for the OpenAI Realtime API.
//
// More details are at https://platform.openai.com/docs/api-reference/realtime-sessions/create.
func (c *OpenAIRealtimeAPI) sendOffer(sdp, ephemeralToken string) (string, error) {
	endpointUrl := fmt.Sprintf("https://api.openai.com/v1/realtime?model=%s", url.QueryEscape(c.Model))
	req, err := http.NewRequest("POST", endpointUrl, strings.NewReader(sdp))
	if err != nil {
		return "", err
	}

	req.Header.Set("Authorization", "Bearer "+ephemeralToken)
	req.Header.Set("Content-Type", "application/sdp")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	if res.StatusCode >= 300 {
		bs, _ := io.ReadAll(res.Body)
		return "", fmt.Errorf("HTTP %d\n\n%s", res.StatusCode, string(bs))
	}

	answer, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}

	return string(answer), nil
}
