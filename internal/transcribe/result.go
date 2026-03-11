package transcribe

import "time"

// TranscriptionResult holds the result of transcribing a single chunk.
type TranscriptionResult struct {
	SeqNum   uint64        // matches the chunk sequence number
	Text     string        // transcribed text
	RawMP3   []byte        // source audio bytes
	Duration time.Duration // chunk duration
	Latency  time.Duration // API call latency
	Err      error         // non-nil if transcription failed
}
