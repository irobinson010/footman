package sherpa

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/irobinson010/footman/internal/registry"
	"github.com/irobinson010/footman/internal/speech/models"
)

func TestTTSOptionsDefaults(t *testing.T) {
	k := TTSOptions{}.withDefaults(KokoroName)
	if k.Model != DefaultKokoroModel || k.Speaker != DefaultKokoroSpeaker || k.Speed != 1 || k.Lexicon != models.TTSLexicon ||
		k.SentencePause != 0.15 || k.FirstWords != 4 || k.Provider != "cpu" || k.Threads < 1 || k.Threads > 8 {
		t.Errorf("kokoro defaults %+v", k)
	}
	if runtime.NumCPU() >= 16 && k.Threads != 8 {
		t.Errorf("threads %d", k.Threads)
	}
	p := TTSOptions{}.withDefaults(PiperName)
	if p.Model != DefaultPiperModel || p.Speaker != "" {
		t.Errorf("piper defaults %+v", p)
	}
	// A speaker is only defaulted for the stock Kokoro model.
	if o := (TTSOptions{Model: "/voices/ashford"}).withDefaults(KokoroName); o.Speaker != "" {
		t.Errorf("speaker defaulted for a custom model: %q", o.Speaker)
	}
	if o := (TTSOptions{FirstWords: -1, Common: Common{Threads: 3}, Speed: 1.5}).withDefaults(KokoroName); o.FirstWords != -1 || o.Threads != 3 || o.Speed != 1.5 {
		t.Errorf("kept options %+v", o)
	}
	var o TTSOptions
	cfg := registry.Map{"models_dir": "/m", "model": "piper-en-us-ljspeech", "speaker": "3", "speed": 0.9, "sentence_pause": 0.3, "first_words": 6}
	if err := cfg.Decode(&o); err != nil {
		t.Fatal(err)
	}
	if o.ModelsDir != "/m" || o.Model != "piper-en-us-ljspeech" || o.Speaker != "3" || o.Speed != 0.9 || o.SentencePause != 0.3 || o.FirstWords != 6 {
		t.Errorf("decoded %+v", o)
	}
}

func touch(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFindModelFiles(t *testing.T) {
	kokoro := t.TempDir()
	touch(t, kokoro, "model.onnx", "model.int8.onnx", "voices.bin", "tokens.txt", "lexicon-gb-en.txt", "lexicon-us-en.txt")
	f, err := findModelFiles(kokoro)
	if err != nil {
		t.Fatal(err)
	}
	if f.family != familyKokoro || filepath.Base(f.model) != "model.onnx" || f.voices == "" || f.lexicons["gb"] == "" || f.lexicons["us"] == "" {
		t.Errorf("kokoro: %+v", f)
	}

	piper := t.TempDir()
	touch(t, piper, "en_US-norman-medium.onnx", "en_US-norman-medium.onnx.json", "tokens.txt")
	f, err = findModelFiles(piper)
	if err != nil {
		t.Fatal(err)
	}
	if f.family != familyVITS || filepath.Base(f.config) != "en_US-norman-medium.onnx.json" || len(f.lexicons) != 0 {
		t.Errorf("piper: %+v", f)
	}
	// A fine-tuned model given as its .onnx file, with its own lexicon.
	touch(t, piper, "ashford.onnx", "lexicon.txt")
	f, err = findModelFiles(filepath.Join(piper, "ashford.onnx"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(f.model) != "ashford.onnx" || f.config != "" || filepath.Base(f.lexicons[""]) != "lexicon.txt" {
		t.Errorf("file: %+v", f)
	}

	empty := t.TempDir()
	if _, err := findModelFiles(empty); err == nil {
		t.Error("folder without tokens.txt accepted")
	}
	touch(t, empty, "tokens.txt")
	if _, err := findModelFiles(empty); err == nil {
		t.Error("folder without a model accepted")
	}
	if _, err := findModelFiles(filepath.Join(empty, "tokens.txt")); err == nil {
		t.Error("non-onnx file accepted")
	}
}

func TestSpeakerID(t *testing.T) {
	names := []string{"af_heart", "bm_george", "bf_emma"}
	for _, c := range []struct {
		speaker string
		names   []string
		n       int
		want    int
		ok      bool
	}{
		{"", names, 3, 0, true},
		{"bm_george", names, 3, 1, true},
		{"BF_Emma", names, 3, 2, true},
		{"2", names, 3, 2, true},
		{"3", names, 3, 0, false},
		{"nobody", names, 3, 0, false},
		{"anyone", nil, 1, 0, true}, // single speaker: ignored
		{"bob", nil, 4, 0, false},
		{"1", nil, 4, 1, true},
	} {
		got, err := speakerID(c.speaker, c.names, c.n)
		if got != c.want || (err == nil) != c.ok {
			t.Errorf("speakerID(%q, %d) = %d, %v", c.speaker, c.n, got, err)
		}
	}
}

func TestSpeakerNames(t *testing.T) {
	if got := speakerNames(map[string]string{"speaker_names": "a,b"}, nil); len(got) != 2 || got[1] != "b" {
		t.Errorf("kokoro: %q", got)
	}
	cfg := []byte(`{"num_speakers": 2, "speaker_id_map": {"p239": 1, "p225": 0}}`)
	if got := speakerNames(nil, cfg); len(got) != 2 || got[0] != "p225" || got[1] != "p239" {
		t.Errorf("piper: %q", got)
	}
	if got := speakerNames(nil, []byte(`{"speaker_id_map": {"x": 5}}`)); got != nil {
		t.Errorf("bad map: %q", got)
	}
}

func TestChooseAccent(t *testing.T) {
	for _, c := range []struct {
		lang, speaker, voice string
		want                 accent
	}{
		{"en-GB", "af_heart", "", british},
		{"en_gb", "", "", british},
		{"en-US", "bm_george", "", american},
		{"", "bm_george", "", british},
		{"", "af_heart", "", american},
		{"", "26", "en-gb-x-rp", british},
		{"", "", "en-us", american},
		{"en", "bf_emma", "", british},
	} {
		if got := chooseAccent(c.lang, c.speaker, c.voice); got != c.want {
			t.Errorf("chooseAccent(%q, %q, %q) = %d", c.lang, c.speaker, c.voice, got)
		}
	}
}

// onnxFile writes a minimal ONNX ModelProto: an ir_version, a graph to
// skip, and metadata_props.
func onnxFile(t *testing.T, graph int, meta map[string]string) string {
	t.Helper()
	field := func(b []byte, num int, payload []byte) []byte {
		b = binary.AppendUvarint(b, uint64(num<<3|2))
		b = binary.AppendUvarint(b, uint64(len(payload)))
		return append(b, payload...)
	}
	var b []byte
	b = binary.AppendUvarint(b, 1<<3) // ir_version (field 1, varint)
	b = binary.AppendUvarint(b, 8)
	b = field(b, 7, make([]byte, graph)) // graph
	for k, v := range meta {
		var e []byte
		e = field(e, 1, []byte(k))
		e = field(e, 2, []byte(v))
		b = field(b, 14, e)
	}
	p := filepath.Join(t.TempDir(), "model.onnx")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestONNXMetadata(t *testing.T) {
	want := map[string]string{"model_type": "kokoro", "speaker_names": "af_heart,bm_george", "sample_rate": "24000"}
	for _, graph := range []int{10, 200 << 10} { // in the read buffer, and seeked past
		got, err := onnxMetadata(onnxFile(t, graph, want))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) || got["speaker_names"] != want["speaker_names"] {
			t.Errorf("graph %d: got %q", graph, got)
		}
	}
	bad := filepath.Join(t.TempDir(), "bad.onnx")
	if err := os.WriteFile(bad, []byte{0x0f, 0xff}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := onnxMetadata(bad); err == nil {
		t.Error("garbage accepted")
	}
	if _, err := onnxMetadata(filepath.Join(t.TempDir(), "missing.onnx")); err == nil {
		t.Error("missing file accepted")
	}
}
