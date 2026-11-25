package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
)

// readWAV reads a 16 kHz mono PCM WAV (16-bit or 32-bit float) into float32
// samples in [-1, 1], which is what both engines take (ADR-0021).
func readWAV(path string) ([]float32, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, errors.New("not a RIFF/WAVE file")
	}
	var (
		format, channels, bits uint16
		rate                   uint32
		data                   []byte
	)
	for p := 12; p+8 <= len(b); {
		id := string(b[p : p+4])
		n := int(binary.LittleEndian.Uint32(b[p+4 : p+8]))
		body := p + 8
		end := body + n
		if end > len(b) {
			end = len(b)
		}
		switch id {
		case "fmt ":
			if end-body < 16 {
				return nil, errors.New("short fmt chunk")
			}
			format = binary.LittleEndian.Uint16(b[body:])
			channels = binary.LittleEndian.Uint16(b[body+2:])
			rate = binary.LittleEndian.Uint32(b[body+4:])
			bits = binary.LittleEndian.Uint16(b[body+14:])
			if format == 0xFFFE && end-body >= 26 { // WAVE_FORMAT_EXTENSIBLE
				format = binary.LittleEndian.Uint16(b[body+24:])
			}
		case "data":
			data = b[body:end]
		}
		p = body + n + n%2
	}
	if rate != 16000 || channels != 1 {
		return nil, fmt.Errorf("want 16 kHz mono, got %d Hz, %d channels", rate, channels)
	}
	switch {
	case format == 1 && bits == 16:
		out := make([]float32, len(data)/2)
		for i := range out {
			out[i] = float32(int16(binary.LittleEndian.Uint16(data[2*i:]))) / 32768
		}
		return out, nil
	case format == 3 && bits == 32:
		out := make([]float32, len(data)/4)
		for i := range out {
			out[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[4*i:]))
		}
		return out, nil
	}
	return nil, fmt.Errorf("unsupported WAV format %d / %d bits", format, bits)
}
