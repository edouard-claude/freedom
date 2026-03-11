package icecast

// MetadataStripper strips ICY metadata from an Icecast stream.
// Icecast interleaves metadata blocks at fixed intervals (metaInt bytes).
// The metadata block starts with a single byte N, followed by N*16 bytes of metadata text.
type MetadataStripper struct {
	metaInt   int // bytes between metadata blocks (from Icy-MetaInt header)
	audioLeft int // audio bytes remaining before next metadata block
	state     stripState
	metaLeft  int // metadata bytes remaining to skip
}

type stripState int

const (
	stateAudio stripState = iota
	stateMeta
)

// NewMetadataStripper creates a stripper with the given metadata interval.
// If metaInt is 0, metadata stripping is disabled (pass-through).
func NewMetadataStripper(metaInt int) *MetadataStripper {
	return &MetadataStripper{
		metaInt:   metaInt,
		audioLeft: metaInt,
		state:     stateAudio,
	}
}

// Strip processes incoming bytes, removing ICY metadata.
// Returns only the audio bytes. The dst slice is appended to and returned.
func (s *MetadataStripper) Strip(dst, src []byte) []byte {
	if s.metaInt == 0 {
		return append(dst, src...)
	}

	i := 0
	for i < len(src) {
		switch s.state {
		case stateAudio:
			n := len(src) - i
			if n > s.audioLeft {
				n = s.audioLeft
			}
			dst = append(dst, src[i:i+n]...)
			i += n
			s.audioLeft -= n
			if s.audioLeft == 0 {
				s.state = stateMeta
				s.metaLeft = -1 // need to read length byte
			}

		case stateMeta:
			if s.metaLeft == -1 {
				// Read the metadata length byte.
				s.metaLeft = int(src[i]) * 16
				i++
				if s.metaLeft == 0 {
					// No metadata, back to audio.
					s.state = stateAudio
					s.audioLeft = s.metaInt
				}
			} else {
				// Skip metadata bytes.
				skip := len(src) - i
				if skip > s.metaLeft {
					skip = s.metaLeft
				}
				i += skip
				s.metaLeft -= skip
				if s.metaLeft == 0 {
					s.state = stateAudio
					s.audioLeft = s.metaInt
				}
			}
		}
	}
	return dst
}
