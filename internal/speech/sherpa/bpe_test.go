package sherpa

import (
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

// spm builds a SentencePiece ModelProto with the given pieces (and an
// unrelated field, as real models have).
func spm(pieces map[string]float32, controls ...string) []byte {
	field := func(num byte, wire byte, body []byte) []byte {
		out := []byte{num<<3 | wire}
		if wire == 2 {
			out = binary.AppendUvarint(out, uint64(len(body)))
		}
		return append(out, body...)
	}
	piece := func(p string, score float32, typ byte) []byte {
		b := field(1, 2, []byte(p))
		b = append(b, field(2, 5, binary.LittleEndian.AppendUint32(nil, math.Float32bits(score)))...)
		if typ != 1 {
			b = append(b, field(3, 0, []byte{typ})...)
		}
		return b
	}
	var out []byte
	for _, c := range controls {
		out = append(out, field(1, 2, piece(c, 0, 3))...)
	}
	for p, s := range pieces {
		out = append(out, field(1, 2, piece(p, s, 1))...)
	}
	return append(out, field(2, 2, []byte("trainer"))...)
}

func TestBPEEncode(t *testing.T) {
	m := spm(map[string]float32{
		"▁": -3, "A": -4, "L": -4, "F": -4, "R": -4, "E": -4, "D": -4,
		"▁A": -5, "RE": -6, "ED": -8, "LF": -20, "FRED": -20,
	}, "<unk>")
	b, err := parseBPE(m)
	if err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{
		"alfred":   "▁A L F RE D", // beats FRED (-25 in all) and ED
		"  Alfred": "▁A L F RE D",
		"red":      "▁ RE D",
		"a d":      "▁A ▁ D",
	} {
		got, err := b.encode(in)
		if err != nil || strings.Join(got, " ") != want {
			t.Errorf("%q = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "   ", "café", "b"} {
		if got, err := b.encode(in); err == nil {
			t.Errorf("%q encoded as %q", in, got)
		}
	}
}

func TestBPERejectsGarbage(t *testing.T) {
	for _, raw := range [][]byte{nil, {0xff}, {0x0a, 0x05, 'a'}, []byte("hello, world")} {
		if _, err := parseBPE(raw); err == nil {
			t.Errorf("%q parsed", raw)
		}
	}
}

func TestKWSOptionsDefaults(t *testing.T) {
	o := KWSOptions{}.withDefaults()
	if o.Model != DefaultKWSModel || o.Boost != 2 || o.Threshold != 0.05 || o.Threads != 1 || o.Provider != "cpu" ||
		len(o.Keywords) != 1 || o.Keywords[0] != "hey footman" {
		t.Errorf("defaults = %+v", o)
	}
	o = KWSOptions{Boost: 4, Threshold: 0.02, Keywords: []string{"ashford"}}.withDefaults()
	if o.Boost != 4 || o.Threshold != 0.02 || o.Keywords[0] != "ashford" {
		t.Errorf("explicit options changed: %+v", o)
	}
}
