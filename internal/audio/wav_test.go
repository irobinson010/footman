package audio_test

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"

	"github.com/irobinson010/footman/internal/audio"
)

func TestWAVRoundTrip(t *testing.T) {
	in := audio.Clip{Format: audio.Format{SampleRate: 8000, Channels: 2}, Samples: []float32{0, 0.5, -0.5, 1, -1, 0.25}}
	var buf bytes.Buffer
	if err := audio.EncodeWAV(&buf, in); err != nil {
		t.Fatal(err)
	}
	out, err := audio.DecodeWAV(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if out.Format != in.Format || len(out.Samples) != len(in.Samples) {
		t.Fatalf("got %+v, want %+v", out, in)
	}
	for i := range in.Samples {
		if d := math.Abs(float64(out.Samples[i] - in.Samples[i])); d > 1e-4 {
			t.Errorf("sample %d: %v, want %v", i, out.Samples[i], in.Samples[i])
		}
	}
}

// wav builds a WAVE file by hand with an extra chunk before data.
func wav(format, channels, rate, bits int, data []byte) []byte {
	var b bytes.Buffer
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	w(uint32(4 + 8 + 16 + 8 + 4 + 8 + len(data)))
	b.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(format))
	w(uint16(channels))
	w(uint32(rate))
	w(uint32(rate * channels * bits / 8))
	w(uint16(channels * bits / 8))
	w(uint16(bits))
	b.WriteString("LIST")
	w(uint32(3)) // odd size: padded to 4
	b.WriteString("abc\x00")
	b.WriteString("data")
	w(uint32(len(data)))
	b.Write(data)
	return b.Bytes()
}

func TestDecodeWAVEncodings(t *testing.T) {
	f32 := make([]byte, 8)
	binary.LittleEndian.PutUint32(f32, math.Float32bits(0.5))
	binary.LittleEndian.PutUint32(f32[4:], math.Float32bits(-0.25))
	for _, tc := range []struct {
		name   string
		file   []byte
		want   []float32
		format audio.Format
	}{
		{"pcm8", wav(1, 1, 16000, 8, []byte{128, 192, 64}), []float32{0, 0.5, -0.5}, audio.Speech},
		{"pcm16", wav(1, 1, 16000, 16, []byte{0, 0x40, 0, 0xC0}), []float32{0.5, -0.5}, audio.Speech},
		{"pcm24", wav(1, 1, 16000, 24, []byte{0, 0, 0x40, 0, 0, 0xC0}), []float32{0.5, -0.5}, audio.Speech},
		{"float32", wav(3, 1, 16000, 32, f32), []float32{0.5, -0.25}, audio.Speech},
		{"stereo", wav(1, 2, 44100, 16, []byte{0, 0x40, 0, 0x40}), []float32{0.5, 0.5}, audio.Format{SampleRate: 44100, Channels: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := audio.DecodeWAV(bytes.NewReader(tc.file))
			if err != nil {
				t.Fatal(err)
			}
			if c.Format != tc.format {
				t.Errorf("format %+v, want %+v", c.Format, tc.format)
			}
			if len(c.Samples) != len(tc.want) {
				t.Fatalf("samples %v, want %v", c.Samples, tc.want)
			}
			for i := range tc.want {
				if math.Abs(float64(c.Samples[i]-tc.want[i])) > 1e-3 {
					t.Errorf("samples %v, want %v", c.Samples, tc.want)
				}
			}
		})
	}
}

func TestDecodeWAVRejects(t *testing.T) {
	for name, b := range map[string][]byte{
		"empty":    nil,
		"not riff": []byte("RIFX\x00\x00\x00\x00WAVE"),
		"no data":  []byte("RIFF\x04\x00\x00\x00WAVE"),
		"adpcm":    wav(2, 1, 16000, 4, []byte{1, 2}),
	} {
		if _, err := audio.DecodeWAV(bytes.NewReader(b)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestToSpeech(t *testing.T) {
	// One second of 48 kHz stereo becomes one second of 16 kHz mono.
	in := audio.Clip{Format: audio.Format{SampleRate: 48000, Channels: 2}, Samples: make([]float32, 96000)}
	for i := 0; i < len(in.Samples); i += 2 {
		in.Samples[i], in.Samples[i+1] = 0.2, 0.4
	}
	out := audio.ToSpeech(in)
	if out.Format != audio.Speech || len(out.Samples) != 16000 {
		t.Fatalf("got %v with %d samples", out.Format, len(out.Samples))
	}
	for _, s := range out.Samples {
		if math.Abs(float64(s-0.3)) > 1e-6 {
			t.Fatalf("sample %v, want 0.3", s)
		}
	}
	same := audio.Clip{Format: audio.Speech, Samples: []float32{1}}
	if got := audio.ToSpeech(same); &got.Samples[0] != &same.Samples[0] {
		t.Error("a clip already in the speech format was copied")
	}
}
