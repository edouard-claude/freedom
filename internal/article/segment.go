package article

// Segment pairs transcribed text with its source audio.
type Segment struct {
	Text   string
	RawMP3 []byte
}

// Window is a sliding window of accumulated segments.
type Window struct {
	Text     string
	RawMP3   []byte
	Segments []Segment
}
