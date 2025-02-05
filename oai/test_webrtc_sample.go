package main

import (
	"fmt"
	"time"

	"github.com/pion/webrtc/v3/pkg/media"
)

func _main() {
	sample := media.Sample{
		Data:     []byte("test"),
		Duration: 20 * time.Millisecond,
	}
	fmt.Println("Sample struct is available:", sample)
}
