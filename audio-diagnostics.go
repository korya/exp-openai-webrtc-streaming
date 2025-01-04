package main

import (
	"fmt"
	"sync"
	"time"
)

type AudioDiagnostics struct {
	sampleCount     int64
	lastPrintTime   time.Time
	packetsReceived int64
	bytesReceived   int64
	mutex           sync.Mutex
}

func NewAudioDiagnostics() *AudioDiagnostics {
	return &AudioDiagnostics{
		lastPrintTime: time.Now(),
	}
}

func (d *AudioDiagnostics) logStats(pcmSamples []int16, opusPayload []byte, decodedSamples int) {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	d.sampleCount += int64(decodedSamples)
	d.packetsReceived++
	d.bytesReceived += int64(len(opusPayload))

	// Print stats every second
	if time.Since(d.lastPrintTime) >= time.Second {
		sampleRate := float64(d.sampleCount) / time.Since(d.lastPrintTime).Seconds()
		fmt.Printf("\nAudio Statistics:\n")
		fmt.Printf("Sample Rate: %.2f Hz\n", sampleRate)
		fmt.Printf("Packets Received: %d\n", d.packetsReceived)
		fmt.Printf("Bytes Received: %d\n", d.bytesReceived)
		min, max := minMax(pcmSamples)
		fmt.Printf("PCM Sample Range: min=%d, max=%d\n", min, max)

		d.sampleCount = 0
		d.packetsReceived = 0
		d.bytesReceived = 0
		d.lastPrintTime = time.Now()
	}
}

func minMax(samples []int16) (min, max int16) {
	if len(samples) == 0 {
		return 0, 0
	}
	min = samples[0]
	max = samples[0]
	for _, s := range samples {
		if s < min {
			min = s
		}
		if s > max {
			max = s
		}
	}
	return min, max
}
