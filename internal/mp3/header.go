package mp3

import "errors"

var (
	ErrInvalidSync       = errors.New("mp3: invalid sync word")
	ErrReservedVersion   = errors.New("mp3: reserved MPEG version")
	ErrReservedLayer     = errors.New("mp3: reserved layer")
	ErrInvalidBitrate    = errors.New("mp3: invalid bitrate index")
	ErrInvalidSampleRate = errors.New("mp3: invalid sample rate index")
)

// Header represents a parsed MP3 frame header (4 bytes).
type Header struct {
	Version    int // MPEGVersion constant
	Layer      int // Layer constant
	Bitrate    int // kbps
	SampleRate int // Hz
	Padding    bool
	Samples    int // samples per frame
}

// ParseHeader parses 4 bytes into an MP3 frame header.
func ParseHeader(b [4]byte) (Header, error) {
	// Check sync word: 11 bits set.
	if b[0] != 0xFF || (b[1]&0xE0) != 0xE0 {
		return Header{}, ErrInvalidSync
	}

	version := int((b[1] >> 3) & 0x03)
	if version == MPEGVersionRsvd {
		return Header{}, ErrReservedVersion
	}

	layer := int((b[1] >> 1) & 0x03)
	if layer == LayerRsvd {
		return Header{}, ErrReservedLayer
	}

	bitrateIdx := int((b[2] >> 4) & 0x0F)
	bitrate := bitrateTable[version][layer][bitrateIdx]
	if bitrate <= 0 {
		return Header{}, ErrInvalidBitrate
	}

	srIdx := int((b[2] >> 2) & 0x03)
	sampleRate := sampleRateTable[version][srIdx]
	if sampleRate == 0 {
		return Header{}, ErrInvalidSampleRate
	}

	padding := (b[2]>>1)&0x01 == 1
	samples := samplesPerFrame[version][layer]

	return Header{
		Version:    version,
		Layer:      layer,
		Bitrate:    bitrate,
		SampleRate: sampleRate,
		Padding:    padding,
		Samples:    samples,
	}, nil
}

// FrameSize returns the size of the full frame in bytes (header + data).
func (h Header) FrameSize() int {
	pad := 0
	if h.Padding {
		if h.Layer == LayerI {
			pad = 4
		} else {
			pad = 1
		}
	}
	if h.Layer == LayerI {
		return (12*h.Bitrate*1000/h.SampleRate + pad) * 4
	}
	return h.Samples/8*h.Bitrate*1000/h.SampleRate + pad
}

// Duration returns the duration of one frame in seconds.
func (h Header) Duration() float64 {
	return float64(h.Samples) / float64(h.SampleRate)
}
