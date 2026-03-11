package mp3

import "time"

// Frame represents a single parsed MP3 frame.
type Frame struct {
	RawBytes   []byte        // complete frame bytes (header + data)
	Bitrate    int           // kbps
	SampleRate int           // Hz
	Duration   time.Duration // duration of this frame
	Samples    int           // samples in this frame
}
