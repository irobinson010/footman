package sherpa

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
)

// bpe turns keyword text into the pieces of a SentencePiece model, the way
// the keyword-spotting model's training did (sherpa-onnx's keyword lists name
// pieces, not words, and its Go API cannot tokenise text itself). It reads
// the model's bpe.model file, a protobuf, with no dependency. Despite the
// file's name the gigaspeech model is a unigram model: each piece has a
// score, and the best-scoring split wins.
type bpe struct {
	score map[string]float32 // piece -> score
}

const spaceMark = "▁" // the SentencePiece word-start mark

// SentencePiece piece types that a split may use.
const (
	pieceNormal      = 1
	pieceUserDefined = 4
)

// maxPieceRunes bounds the length of a piece the search tries.
const maxPieceRunes = 16

var errNotSPM = errors.New("not a SentencePiece model")

// parseBPE reads the pieces from a serialized SentencePiece ModelProto.
func parseBPE(raw []byte) (*bpe, error) {
	b := &bpe{score: map[string]float32{}}
	for len(raw) > 0 {
		tag, n := binary.Uvarint(raw)
		if n <= 0 {
			return nil, errNotSPM
		}
		raw = raw[n:]
		var val []byte
		switch tag & 7 {
		case 0: // varint
			if _, n = binary.Uvarint(raw); n <= 0 {
				return nil, errNotSPM
			}
			raw = raw[n:]
			continue
		case 1: // 64-bit
			if len(raw) < 8 {
				return nil, errNotSPM
			}
			raw = raw[8:]
			continue
		case 5: // 32-bit
			if len(raw) < 4 {
				return nil, errNotSPM
			}
			raw = raw[4:]
			continue
		case 2: // length-delimited
			l, n := binary.Uvarint(raw)
			if n <= 0 || uint64(len(raw)-n) < l {
				return nil, errNotSPM
			}
			val, raw = raw[n:n+int(l)], raw[n+int(l):]
		default:
			return nil, errNotSPM
		}
		if tag>>3 != 1 { // field 1 holds the pieces; the rest is not needed
			continue
		}
		piece, score, typ, err := parsePiece(val)
		if err != nil {
			return nil, err
		}
		if typ == pieceNormal || typ == pieceUserDefined {
			b.score[piece] = score
		}
	}
	if len(b.score) == 0 {
		return nil, errNotSPM
	}
	return b, nil
}

func parsePiece(raw []byte) (piece string, score float32, typ uint64, err error) {
	typ = pieceNormal // the default when the field is absent
	for len(raw) > 0 {
		tag, n := binary.Uvarint(raw)
		if n <= 0 {
			return "", 0, 0, errNotSPM
		}
		raw = raw[n:]
		switch tag {
		case 1<<3 | 2: // piece
			l, n := binary.Uvarint(raw)
			if n <= 0 || uint64(len(raw)-n) < l {
				return "", 0, 0, errNotSPM
			}
			piece, raw = string(raw[n:n+int(l)]), raw[n+int(l):]
		case 2<<3 | 5: // score
			if len(raw) < 4 {
				return "", 0, 0, errNotSPM
			}
			score, raw = math.Float32frombits(binary.LittleEndian.Uint32(raw)), raw[4:]
		case 3 << 3: // type
			if typ, n = binary.Uvarint(raw); n <= 0 {
				return "", 0, 0, errNotSPM
			}
			raw = raw[n:]
		default:
			return "", 0, 0, errNotSPM
		}
	}
	return piece, score, typ, nil
}

// encode splits text into pieces. The text is upper-cased and its spaces
// collapsed, as the model's vocabulary is upper case. The split is the one
// with the best total score (Viterbi), as SentencePiece does for a unigram
// model. A character the model has no piece for is an error.
func (b *bpe) encode(text string) ([]string, error) {
	words := strings.Fields(strings.ToUpper(text))
	if len(words) == 0 {
		return nil, errors.New("empty text")
	}
	runes := []rune(spaceMark + strings.Join(words, spaceMark))
	for _, r := range runes {
		if _, ok := b.score[string(r)]; !ok {
			return nil, fmt.Errorf("the keyword model has no piece for %q", r)
		}
	}
	// best[i] is the best score of the first i characters, from[i] where the
	// last piece started. Every character is a piece, so every prefix has a
	// split.
	n := len(runes)
	best := make([]float64, n+1)
	from := make([]int, n+1)
	for i := 1; i <= n; i++ {
		best[i] = math.Inf(-1)
		for j := max(0, i-maxPieceRunes); j < i; j++ {
			sc, ok := b.score[string(runes[j:i])]
			if !ok {
				continue
			}
			if v := best[j] + float64(sc); v > best[i] {
				best[i], from[i] = v, j
			}
		}
	}
	var out []string
	for i := n; i > 0; i = from[i] {
		out = append(out, string(runes[from[i]:i]))
	}
	slices.Reverse(out)
	return out, nil
}
