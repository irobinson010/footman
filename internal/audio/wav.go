package audio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

// ReadWAVFile decodes the WAV file at path. See DecodeWAV.
func ReadWAVFile(path string) (Clip, error) {
	f, err := os.Open(path)
	if err != nil {
		return Clip{}, err
	}
	defer func() { _ = f.Close() }()
	c, err := DecodeWAV(f)
	if err != nil {
		return Clip{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// DecodeWAV reads a RIFF WAVE stream holding 8, 16, 24 or 32-bit integer PCM
// or 32 or 64-bit float PCM, at any rate and channel count, and returns it as
// float32 samples in its own format. Use ToSpeech to get 16 kHz mono.
func DecodeWAV(r io.Reader) (Clip, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Clip{}, fmt.Errorf("wav: header: %w", err)
	}
	if string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return Clip{}, errors.New("wav: not a RIFF WAVE file")
	}
	var (
		format   uint16
		channels int
		rate     int
		bits     int
		haveFmt  bool
	)
	for {
		var ch [8]byte
		if _, err := io.ReadFull(r, ch[:]); err != nil {
			return Clip{}, fmt.Errorf("wav: no data chunk: %w", err)
		}
		id, size := string(ch[0:4]), binary.LittleEndian.Uint32(ch[4:8])
		switch id {
		case "fmt ":
			if size < 16 || size > 1<<16 {
				return Clip{}, fmt.Errorf("wav: bad fmt chunk size %d", size)
			}
			b := make([]byte, size+size%2)
			if _, err := io.ReadFull(r, b); err != nil {
				return Clip{}, fmt.Errorf("wav: fmt chunk: %w", err)
			}
			format = binary.LittleEndian.Uint16(b[0:2])
			channels = int(binary.LittleEndian.Uint16(b[2:4]))
			rate = int(binary.LittleEndian.Uint32(b[4:8]))
			bits = int(binary.LittleEndian.Uint16(b[14:16]))
			if format == 0xFFFE && size >= 26 { // WAVE_FORMAT_EXTENSIBLE: the sub-format's first two bytes
				format = binary.LittleEndian.Uint16(b[24:26])
			}
			haveFmt = true
		case "data":
			if !haveFmt {
				return Clip{}, errors.New("wav: data chunk before fmt chunk")
			}
			return decodePCM(r, size, format, channels, rate, bits)
		default:
			if _, err := io.CopyN(io.Discard, r, int64(size)+int64(size%2)); err != nil {
				return Clip{}, fmt.Errorf("wav: %q chunk: %w", id, err)
			}
		}
	}
}

func decodePCM(r io.Reader, size uint32, format uint16, channels, rate, bits int) (Clip, error) {
	f := Format{SampleRate: rate, Channels: channels}
	if !f.Valid() {
		return Clip{}, fmt.Errorf("wav: bad format: %d Hz, %d channels", rate, channels)
	}
	width := bits / 8
	switch {
	case format == 1 && (bits == 8 || bits == 16 || bits == 24 || bits == 32):
	case format == 3 && (bits == 32 || bits == 64):
	default:
		return Clip{}, fmt.Errorf("wav: unsupported encoding (format %d, %d bits)", format, bits)
	}
	// Streams written live often leave the size as 0 or 0xFFFFFFFF; read to
	// the end then.
	var data []byte
	var err error
	if size == 0 || size == math.MaxUint32 {
		data, err = io.ReadAll(r)
	} else {
		data = make([]byte, size)
		var n int
		n, err = io.ReadFull(r, data)
		if errors.Is(err, io.ErrUnexpectedEOF) {
			data, err = data[:n], nil // truncated file: keep what is there
		}
	}
	if err != nil {
		return Clip{}, fmt.Errorf("wav: data: %w", err)
	}
	n := len(data) / width
	n -= n % channels
	out := make([]float32, n)
	for i := range out {
		b := data[i*width:]
		switch {
		case format == 3 && bits == 32:
			out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b))
		case format == 3:
			out[i] = float32(math.Float64frombits(binary.LittleEndian.Uint64(b)))
		case bits == 8:
			out[i] = (float32(b[0]) - 128) / 128
		case bits == 16:
			out[i] = float32(int16(binary.LittleEndian.Uint16(b))) / 32768
		case bits == 24:
			v := int32(uint32(b[0])<<8|uint32(b[1])<<16|uint32(b[2])<<24) >> 8
			out[i] = float32(v) / (1 << 23)
		default:
			out[i] = float32(int32(binary.LittleEndian.Uint32(b))) / (1 << 31)
		}
	}
	return Clip{Format: f, Samples: out}, nil
}

// EncodeWAV writes c as 16-bit integer PCM WAVE, clipping samples to [-1, 1].
func EncodeWAV(w io.Writer, c Clip) error {
	if !c.Format.Valid() {
		return errors.New("wav: invalid format")
	}
	n := len(c.Samples) * 2
	b := make([]byte, 44+n)
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+n))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], uint16(c.Format.Channels))
	binary.LittleEndian.PutUint32(b[24:], uint32(c.Format.SampleRate))
	binary.LittleEndian.PutUint32(b[28:], uint32(c.Format.SampleRate*c.Format.Channels*2))
	binary.LittleEndian.PutUint16(b[32:], uint16(c.Format.Channels*2))
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(n))
	for i, s := range c.Samples {
		s = max(-1, min(1, s))
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(int16(math.Round(float64(s)*32767))))
	}
	_, err := w.Write(b)
	return err
}

// ToSpeech returns c as 16 kHz mono (the Speech format): channels are
// averaged and the rate is converted by linear interpolation, which is
// adequate for speech recognition. A clip already in that format is returned
// as is.
func ToSpeech(c Clip) Clip {
	return Resample(Mono(c), Speech.SampleRate)
}

// Mono averages the channels of c into one.
func Mono(c Clip) Clip {
	ch := c.Format.Channels
	if ch <= 1 {
		return c
	}
	out := make([]float32, len(c.Samples)/ch)
	for i := range out {
		var sum float32
		for _, s := range c.Samples[i*ch : i*ch+ch] {
			sum += s
		}
		out[i] = sum / float32(ch)
	}
	return Clip{Format: Format{SampleRate: c.Format.SampleRate, Channels: 1}, Samples: out}
}

// Resample converts mono clip c to rate by linear interpolation. It returns c
// unchanged if it is already at rate or is not mono.
func Resample(c Clip, rate int) Clip {
	from := c.Format.SampleRate
	if from == rate || from <= 0 || rate <= 0 || c.Format.Channels != 1 || len(c.Samples) == 0 {
		return c
	}
	n := int(int64(len(c.Samples)) * int64(rate) / int64(from))
	out := make([]float32, n)
	step := float64(from) / float64(rate)
	last := len(c.Samples) - 1
	for i := range out {
		pos := float64(i) * step
		j := int(pos)
		if j >= last {
			out[i] = c.Samples[last]
			continue
		}
		frac := float32(pos - float64(j))
		out[i] = c.Samples[j]*(1-frac) + c.Samples[j+1]*frac
	}
	return Clip{Format: Format{SampleRate: rate, Channels: 1}, Samples: out}
}
