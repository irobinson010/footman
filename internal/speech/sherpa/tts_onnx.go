package sherpa

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// onnxMetadata reads the metadata_props of an ONNX model file: the key/value
// pairs sherpa-onnx's export scripts write (model_type, sample_rate,
// speaker_names and so on). It reads only the top level of the protobuf and
// seeks past the graph, so it is cheap even for a large model.
func onnxMetadata(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	r := &seekReader{f: f, br: bufio.NewReaderSize(f, 64<<10)}
	meta := map[string]string{}
	for {
		tag, err := binary.ReadUvarint(r.br)
		if errors.Is(err, io.EOF) {
			return meta, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s: not an ONNX model: %w", path, err)
		}
		field, wire := tag>>3, tag&7
		switch wire {
		case 0:
			if _, err := binary.ReadUvarint(r.br); err != nil {
				return nil, fmt.Errorf("%s: not an ONNX model: %w", path, err)
			}
		case 1:
			err = r.skip(8)
		case 5:
			err = r.skip(4)
		case 2:
			n, err := binary.ReadUvarint(r.br)
			if err != nil {
				return nil, fmt.Errorf("%s: not an ONNX model: %w", path, err)
			}
			if field != 14 { // ModelProto.metadata_props
				if err := r.skip(int64(n)); err != nil {
					return nil, fmt.Errorf("%s: not an ONNX model: %w", path, err)
				}
				continue
			}
			if n > 64<<20 {
				return nil, fmt.Errorf("%s: metadata entry too large", path)
			}
			b := make([]byte, n)
			if _, err := io.ReadFull(r.br, b); err != nil {
				return nil, fmt.Errorf("%s: not an ONNX model: %w", path, err)
			}
			k, v, err := stringEntry(b)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			meta[k] = v
		default:
			return nil, fmt.Errorf("%s: not an ONNX model (wire type %d)", path, wire)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: not an ONNX model: %w", path, err)
		}
	}
}

// stringEntry decodes an ONNX StringStringEntryProto (key = 1, value = 2).
func stringEntry(b []byte) (key, value string, err error) {
	for len(b) > 0 {
		tag, n := binary.Uvarint(b)
		if n <= 0 {
			return "", "", errors.New("bad metadata entry")
		}
		b = b[n:]
		if tag&7 != 2 {
			return "", "", errors.New("bad metadata entry")
		}
		l, n := binary.Uvarint(b)
		if n <= 0 || uint64(len(b)-n) < l {
			return "", "", errors.New("bad metadata entry")
		}
		s := string(b[n : n+int(l)])
		b = b[n+int(l):]
		switch tag >> 3 {
		case 1:
			key = s
		case 2:
			value = s
		}
	}
	return key, value, nil
}

// seekReader skips large fields by seeking instead of reading them.
type seekReader struct {
	f  *os.File
	br *bufio.Reader
}

func (r *seekReader) skip(n int64) error {
	if n <= int64(r.br.Buffered()) {
		_, err := r.br.Discard(int(n))
		return err
	}
	n -= int64(r.br.Buffered())
	if _, err := r.f.Seek(n, io.SeekCurrent); err != nil {
		return err
	}
	r.br.Reset(r.f)
	return nil
}
