package sherpa

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/irobinson010/footman/internal/speech/models"
)

// TTS registry names and defaults (ADR-0037). Both names are the same
// sherpa-onnx engine; the name picks the default model, and the model's
// files decide how it is run, so a persona's `[voice] engine` and `model`
// can never disagree in a way that matters.
const (
	// KokoroName is the Kokoro (StyleTTS2) text-to-speech engine.
	KokoroName = "kokoro"
	// PiperName is the Piper (VITS) text-to-speech engine.
	PiperName = "piper"
	// DefaultKokoroModel is the kokoro engine's default model.
	DefaultKokoroModel = "kokoro-en"
	// DefaultKokoroSpeaker is the kokoro engine's default speaker: a
	// British male voice, the interim Ashford.
	DefaultKokoroSpeaker = "bm_george"
	// DefaultPiperModel is the piper engine's default model.
	DefaultPiperModel = "piper-en-us-norman"
)

// Model families.
const (
	familyKokoro = "kokoro"
	familyVITS   = "vits"
)

// TTSOptions configure the kokoro and piper engines. Every field is a
// default that a speech.Voice can override per call.
type TTSOptions struct {
	Common
	// Model is the default voice model: a catalog ID or a path to a model
	// folder or .onnx file. Default kokoro-en (kokoro) or
	// piper-en-us-norman (piper).
	Model string `json:"model"`
	// Speaker is the default speaker of a multi-speaker model, by name or
	// index (default bm_george for kokoro-en).
	Speaker string `json:"speaker"`
	// Speed scales the speaking rate (default 1).
	Speed float32 `json:"speed"`
	// Lexicon is the fallback pronunciation lexicon (espeak-ng IPA, as
	// lexicon-en-us.txt): a catalog ID or path. Default tts-lexicon-en-us.
	// Piper models need it; Kokoro uses it for words its own lexicons lack.
	Lexicon string `json:"lexicon"`
	// SentencePause is the silence between sentences, in seconds (default
	// 0.15).
	SentencePause float32 `json:"sentence_pause"`
	// FirstWords: the first sentence is cut at its first comma (or ; :)
	// after this many words, so the first audio comes sooner (default 4;
	// -1 never cuts it).
	FirstWords int `json:"first_words"`
	// Warmup synthesizes one short sentence at load (default false).
	Warmup bool `json:"warmup"`
}

func (o TTSOptions) withDefaults(engine string) TTSOptions {
	if o.Model == "" {
		o.Model = DefaultKokoroModel
		if engine == PiperName {
			o.Model = DefaultPiperModel
		}
	}
	if o.Speaker == "" && o.Model == DefaultKokoroModel {
		o.Speaker = DefaultKokoroSpeaker
	}
	if o.Speed <= 0 {
		o.Speed = 1
	}
	if o.Lexicon == "" {
		o.Lexicon = models.TTSLexicon
	}
	if o.SentencePause <= 0 {
		o.SentencePause = 0.15
	}
	if o.FirstWords == 0 {
		o.FirstWords = 4
	}
	// Kokoro on a 12-core desktop CPU: 8 threads is the fastest; more is slower (S3-02).
	o.Common = o.Common.withDefaults(max(1, min(8, runtime.NumCPU()/2)))
	return o
}

// modelFiles are the files of one TTS model, found in its folder.
type modelFiles struct {
	family string
	dir    string
	model  string
	tokens string
	// voices is Kokoro's voices.bin.
	voices string
	// lexicons are the model's own lexicons: for Kokoro, by accent
	// ("gb", "us"); for VITS, "" for any lexicon*.txt in the folder.
	lexicons map[string]string
	// config is a Piper model's .onnx.json, if present.
	config string
}

// findModelFiles works out what kind of model path is and where its files
// are. path is a model folder or an .onnx file in one. A folder with
// voices.bin is Kokoro; one with an .onnx model and tokens.txt is VITS
// (Piper). A fine-tuned model in the same layout loads the same way.
func findModelFiles(path string) (modelFiles, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return modelFiles{}, err
	}
	f := modelFiles{dir: path, lexicons: map[string]string{}}
	if !fi.IsDir() {
		if !strings.HasSuffix(path, ".onnx") {
			return modelFiles{}, fmt.Errorf("%s is not an .onnx model or a model folder", path)
		}
		f.dir, f.model = filepath.Dir(path), path
	}
	exists := func(name string) string {
		p := filepath.Join(f.dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
		return ""
	}
	f.tokens = exists("tokens.txt")
	if f.tokens == "" {
		return modelFiles{}, fmt.Errorf("no tokens.txt in %s", f.dir)
	}
	onnx, _ := filepath.Glob(filepath.Join(f.dir, "*.onnx"))
	if f.model == "" {
		f.model = pickONNX(onnx)
	}
	if f.model == "" {
		return modelFiles{}, fmt.Errorf("no .onnx model in %s", f.dir)
	}
	if v := exists("voices.bin"); v != "" {
		f.family, f.voices = familyKokoro, v
		if l := exists("lexicon-gb-en.txt"); l != "" {
			f.lexicons["gb"] = l
		}
		if l := exists("lexicon-us-en.txt"); l != "" {
			f.lexicons["us"] = l
		}
		return f, nil
	}
	f.family = familyVITS
	if c := exists(filepath.Base(f.model) + ".json"); c != "" {
		f.config = c
	}
	if ls, _ := filepath.Glob(filepath.Join(f.dir, "lexicon*.txt")); len(ls) > 0 {
		f.lexicons[""] = ls[0]
	}
	return f, nil
}

// pickONNX chooses the model among a folder's .onnx files: the full
// precision one when there is a choice (int8 Kokoro is slower on the CPU).
func pickONNX(files []string) string {
	slices.Sort(files)
	for _, f := range files {
		if !strings.Contains(filepath.Base(f), "int8") {
			return f
		}
	}
	if len(files) > 0 {
		return files[0]
	}
	return ""
}

// speakerID resolves a speaker given by name or index against the model's
// speaker names (in id order). "" is speaker 0. A single-speaker model
// accepts any speaker and uses its only one, so a persona that switches
// from a stock voice to its own fine-tuned model keeps working.
func speakerID(speaker string, names []string, n int) (int, error) {
	if n <= 1 || speaker == "" {
		return 0, nil
	}
	if i, err := strconv.Atoi(speaker); err == nil {
		if i < 0 || i >= n {
			return 0, fmt.Errorf("speaker %d is out of range (the model has %d)", i, n)
		}
		return i, nil
	}
	for i, name := range names {
		if strings.EqualFold(name, speaker) {
			return i, nil
		}
	}
	if len(names) == 0 {
		return 0, fmt.Errorf("speaker %q: the model has no speaker names, give an index (0 to %d)", speaker, n-1)
	}
	return 0, fmt.Errorf("unknown speaker %q (the model has %s)", speaker, strings.Join(names, ", "))
}

// speakerNames reads the speaker names from a model's metadata: Kokoro's
// speaker_names, or a Piper .onnx.json speaker_id_map.
func speakerNames(meta map[string]string, piperConfig []byte) []string {
	if s := meta["speaker_names"]; s != "" {
		return strings.Split(s, ",")
	}
	return piperSpeakers(piperConfig)
}

// piperSpeakers returns the speaker names of a multi-speaker Piper model's
// .onnx.json, in id order, or nil.
func piperSpeakers(config []byte) []string {
	var c struct {
		SpeakerIDMap map[string]int `json:"speaker_id_map"`
	}
	if len(config) == 0 || json.Unmarshal(config, &c) != nil || len(c.SpeakerIDMap) == 0 {
		return nil
	}
	names := make([]string, len(c.SpeakerIDMap))
	for name, id := range c.SpeakerIDMap {
		if id < 0 || id >= len(names) {
			return nil
		}
		names[id] = name
	}
	return names
}

// chooseAccent picks British or American pronunciation for a voice: from
// the language if it names one ("en-GB"), else from a Kokoro speaker name's
// first letter (b for British, a for American), else from the model's own
// language metadata.
func chooseAccent(language, speaker, modelVoice string) accent {
	l := strings.ToLower(strings.ReplaceAll(language, "_", "-"))
	switch {
	case l == "en-gb" || strings.HasPrefix(l, "en-gb-"):
		return british
	case strings.HasPrefix(l, "en-"):
		return american
	}
	if len(speaker) < 3 || speaker[2] != '_' { // Kokoro names look like bm_george
		speaker = ""
	}
	switch {
	case strings.HasPrefix(speaker, "b"):
		return british
	case strings.HasPrefix(speaker, "a"):
		return american
	}
	if strings.HasPrefix(strings.ToLower(modelVoice), "en-gb") {
		return british
	}
	return american
}
