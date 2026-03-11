package mp3

// MPEG version enum.
const (
	MPEGVersion2_5  = 0 // MPEG 2.5
	MPEGVersionRsvd = 1 // reserved
	MPEGVersion2    = 2 // MPEG 2
	MPEGVersion1    = 3 // MPEG 1
)

// Layer enum.
const (
	LayerRsvd = 0 // reserved
	LayerIII  = 1
	LayerII   = 2
	LayerI    = 3
)

// bitrateTable[version][layer][index] in kbps.
// Index 0 = free, index 15 = bad. version: 0=V2.5, 2=V2, 3=V1. layer: 1=III, 2=II, 3=I.
var bitrateTable = [4][4][16]int{
	// MPEG 2.5 (index 0) -- same rates as MPEG 2
	{
		{},                                                                        // reserved layer
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, -1},      // Layer III
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, -1},      // Layer II
		{0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256, -1}, // Layer I
	},
	{}, // reserved version (index 1)
	// MPEG 2 (index 2)
	{
		{},                                                                        // reserved layer
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, -1},      // Layer III
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, -1},      // Layer II
		{0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256, -1}, // Layer I
	},
	// MPEG 1 (index 3)
	{
		{},                                                                            // reserved layer
		{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, -1},      // Layer III
		{0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, -1},     // Layer II
		{0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448, -1},  // Layer I
	},
}

// sampleRateTable[version][index] in Hz.
var sampleRateTable = [4][4]int{
	{11025, 12000, 8000, 0},  // MPEG 2.5
	{0, 0, 0, 0},             // reserved
	{22050, 24000, 16000, 0}, // MPEG 2
	{44100, 48000, 32000, 0}, // MPEG 1
}

// samplesPerFrame[version][layer].
var samplesPerFrame = [4][4]int{
	// MPEG 2.5
	{0, 576, 1152, 384},
	// reserved
	{0, 0, 0, 0},
	// MPEG 2
	{0, 576, 1152, 384},
	// MPEG 1
	{0, 1152, 1152, 384},
}
