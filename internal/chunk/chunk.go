package chunk

import "time"

// Chunk represents an accumulated segment of MP3 audio ready for transcription.
type Chunk struct {
	SeqNum   uint64        // monotonically increasing sequence number
	RawMP3   []byte        // raw MP3 bytes (concatenated frames)
	Duration time.Duration // total audio duration
}
