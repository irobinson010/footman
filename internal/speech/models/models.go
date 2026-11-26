// Package models is the catalog of downloadable speech models and the
// downloader that fetches them into the models folder (design §10:
// <data>/models), checking each against a pinned SHA-256.
//
// It is pure Go: the engines that load the models live in the native
// packages (internal/speech/whispercpp, internal/speech/sherpa), which use
// Lookup and Model.Path to find their files.
package models

import (
	"archive/tar"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Engine families a model belongs to.
const (
	// WhisperCpp models are single ggml files for the whispercpp engine.
	WhisperCpp = "whispercpp"
	// Sherpa models are folders of ONNX files for the sherpa engines.
	Sherpa = "sherpa"
)

// Model is one downloadable model.
type Model struct {
	// ID is the name used in config (speech.stt.model) and on the command
	// line, such as "whisper-turbo-q5".
	ID string
	// Engine is the family that loads it (WhisperCpp or Sherpa).
	Engine string
	// Kind is what the model does: "stt", "vad", "kws" (wake word), "tts"
	// (a text-to-speech voice) or "lexicon" (a pronunciation lexicon the
	// TTS voices share).
	Kind string
	// File is the file or folder name inside the models folder.
	File string
	// URL is where it is downloaded from.
	URL string
	// SHA256 is the hex digest of the downloaded file (the archive, for
	// archives).
	SHA256 string
	// Size is the download size in bytes.
	Size int64
	// Archive is "tar.bz2" when the download is an archive holding the File
	// folder, or "" for a single file.
	Archive string
	// English is true for English-only models.
	English bool
	// License is the model's license.
	License string
	// Requires lists the catalog IDs Fetch downloads first because this
	// model needs them (a TTS voice's pronunciation lexicon).
	Requires []string
}

// Catalog lists every model Footman knows how to fetch. The checksums are
// those of the files fetched for spikes S0-07 and S0-08 and for S3-02 (for
// ggml files, the Hugging Face LFS object ID).
var Catalog = []Model{
	{ID: "whisper-turbo-q5", Engine: WhisperCpp, Kind: "stt", File: "ggml-large-v3-turbo-q5_0.bin",
		URL:    "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo-q5_0.bin",
		SHA256: "394221709cd5ad1f40c46e6031ca61bce88931e6e088c188294c6d5a55ffa7e2", Size: 574041195, License: "MIT"},
	{ID: "whisper-turbo", Engine: WhisperCpp, Kind: "stt", File: "ggml-large-v3-turbo.bin",
		URL:    "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo.bin",
		SHA256: "1fc70f774d38eb169993ac391eea357ef47c88757ef72ee5943879b7e8e2bc69", Size: 1624555275, License: "MIT"},
	{ID: "whisper-small", Engine: WhisperCpp, Kind: "stt", File: "ggml-small.bin",
		URL:    "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small.bin",
		SHA256: "1be3a9b2063867b937e64e2ec7483364a79917e157fa98c5d94b5c1fffea987b", Size: 487601967, License: "MIT"},
	{ID: "whisper-small.en", Engine: WhisperCpp, Kind: "stt", File: "ggml-small.en.bin", English: true,
		URL:    "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small.en.bin",
		SHA256: "c6138d6d58ecc8322097e0f987c32f1be8bb0a18532a3f88f734d1bbf9c41e5d", Size: 487614201, License: "MIT"},
	{ID: "whisper-base.en", Engine: WhisperCpp, Kind: "stt", File: "ggml-base.en.bin", English: true,
		URL:    "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base.en.bin",
		SHA256: "a03779c86df3323075f5e796cb2ce5029f00ec8869eee3fdfb897afe36c6d002", Size: 147964211, License: "MIT"},
	{ID: "whisper-tiny.en", Engine: WhisperCpp, Kind: "stt", File: "ggml-tiny.en.bin", English: true,
		URL:    "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-tiny.en.bin",
		SHA256: "921e4cf8686fdd993dcd081a5da5b6c365bfde1162e72b08d75ac75289920b1f", Size: 77704715, License: "MIT"},
	{ID: "parakeet-tdt-0.6b-v2", Engine: Sherpa, Kind: "stt", File: "sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8", English: true,
		URL:    "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8.tar.bz2",
		SHA256: "6fc4426b54aa549a0019b8a75b8dea59164e71790a37ac56a045ad57f915f714", Size: 482440461,
		Archive: "tar.bz2", License: "CC-BY-4.0"},
	{ID: "silero-vad", Engine: Sherpa, Kind: "vad", File: "silero_vad.onnx",
		URL:    "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/silero_vad.onnx",
		SHA256: "9e2449e1087496d8d4caba907f23e0bd3f78d91fa552479bb9c23ac09cbb1fd6", Size: 643854, License: "MIT"},
	{ID: "kws-zipformer-gigaspeech", Engine: Sherpa, Kind: "kws", File: "sherpa-onnx-kws-zipformer-gigaspeech-3.3M-2024-01-01", English: true,
		URL:    "https://github.com/k2-fsa/sherpa-onnx/releases/download/kws-models/sherpa-onnx-kws-zipformer-gigaspeech-3.3M-2024-01-01.tar.bz2",
		SHA256: "f170013b4716e41b62b9bfd809687c207cef798ef9bc6534d524e17af9b6561a", Size: 17626723,
		Archive: "tar.bz2", License: "Apache-2.0"},

	// Text-to-speech voices (ADR-0037): the interim stock voices until the
	// trained persona voices exist. A voice's License covers its weights and
	// training data; the shared lexicon's is its own.
	{ID: "kokoro-en", Engine: Sherpa, Kind: "tts", File: "kokoro-multi-lang-v1_0", English: true,
		URL:    "https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/kokoro-multi-lang-v1_0.tar.bz2",
		SHA256: "60703707bc05ccd48d60e1eb723f38b07d6e474795a0b6a588d7220400e0f7a4", Size: 349886476,
		Archive: "tar.bz2", License: "Apache-2.0", Requires: []string{TTSLexicon}},
	{ID: "piper-en-us-norman", Engine: Sherpa, Kind: "tts", File: "vits-piper-en_US-norman-medium", English: true,
		URL:    "https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/vits-piper-en_US-norman-medium.tar.bz2",
		SHA256: "1f32065d480abe9abc7c7f91442125d0b34c1cc065d1e600466cac408eabf3b8", Size: 67203672,
		Archive: "tar.bz2", License: "MIT weights, public-domain data", Requires: []string{TTSLexicon}},
	{ID: "piper-en-us-ljspeech", Engine: Sherpa, Kind: "tts", File: "vits-piper-en_US-ljspeech-medium", English: true,
		URL:    "https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/vits-piper-en_US-ljspeech-medium.tar.bz2",
		SHA256: "3dfb4b759d8be032a4903a9538d128b0fda2a06ab1de6cbc2d93a97e2dd83dba", Size: 67169893,
		Archive: "tar.bz2", License: "MIT weights, public-domain data", Requires: []string{TTSLexicon}},
	{ID: TTSLexicon, Engine: Sherpa, Kind: "lexicon", File: "lexicon-en-us.txt", English: true,
		URL:    "https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/lexicon-en-us.txt",
		SHA256: "8b67ae549a465eee78f92ecb74644ad90c3c58ce65f19c68baf85878a10ef73a", Size: 6305773,
		License: "unconfirmed: CMUdict + espeak-ng data (ADR-0037)"},
}

// TTSLexicon is the catalog ID of the American English pronunciation
// lexicon (espeak-ng IPA) that the TTS voices share.
const TTSLexicon = "tts-lexicon-en-us"

// Lookup returns the catalog entry with the given ID.
func Lookup(id string) (Model, bool) {
	for _, m := range Catalog {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}

// IDs returns the catalog's model IDs, sorted.
func IDs() []string {
	ids := make([]string, len(Catalog))
	for i, m := range Catalog {
		ids[i] = m.ID
	}
	sort.Strings(ids)
	return ids
}

// Path is where the model lives inside the models folder dir.
func (m Model) Path(dir string) string { return filepath.Join(dir, m.File) }

// Installed reports whether the model is in dir. Downloads are moved into
// place only once verified, so presence (and, for a file, the right size)
// means a complete model.
func (m Model) Installed(dir string) bool {
	fi, err := os.Stat(m.Path(dir))
	if err != nil {
		return false
	}
	if m.Archive != "" {
		return fi.IsDir()
	}
	return fi.Mode().IsRegular() && fi.Size() == m.Size
}

// MissingError reports a model that has not been downloaded.
type MissingError struct {
	Model Model
	Dir   string
}

func (e *MissingError) Error() string {
	return fmt.Sprintf("model %s is not downloaded (expected %s); run `footman models fetch %s`",
		e.Model.ID, e.Model.Path(e.Dir), e.Model.ID)
}

// Resolve finds a model given as a catalog ID or as a path to a file or
// folder. For an ID it returns the path inside dir, or a *MissingError if it
// has not been downloaded; a path is returned as is if it exists. engine, if
// not empty, must match the catalog entry's engine family.
func Resolve(nameOrPath, dir, engine string) (string, Model, error) {
	if m, ok := Lookup(nameOrPath); ok {
		if engine != "" && m.Engine != engine {
			return "", m, fmt.Errorf("model %s is for the %s engine, not %s", m.ID, m.Engine, engine)
		}
		if !m.Installed(dir) {
			return "", m, &MissingError{Model: m, Dir: dir}
		}
		return m.Path(dir), m, nil
	}
	if strings.ContainsRune(nameOrPath, filepath.Separator) || filepath.IsAbs(nameOrPath) {
		if _, err := os.Stat(nameOrPath); err != nil {
			return "", Model{}, fmt.Errorf("model file: %w", err)
		}
		return nameOrPath, Model{ID: filepath.Base(nameOrPath), Engine: engine}, nil
	}
	return "", Model{}, fmt.Errorf("unknown model %q (known: %s; or give a path)", nameOrPath, strings.Join(IDs(), ", "))
}

// FetchOptions configure Fetch.
type FetchOptions struct {
	// Client makes the request (default http.DefaultClient).
	Client *http.Client
	// Progress, if set, is called as bytes arrive.
	Progress func(done, total int64)
	// Record, if set, is called with the model's path just before the
	// download starts and with the models folder before that, so the
	// install receipt lists them (ADR-0023) even if the download is
	// interrupted.
	Record func(path string, isDir bool) error
}

// Fetch downloads m into dir unless it is already there, verifying its
// SHA-256, and returns its path. Archives are unpacked; the archive itself
// is not kept. Nothing appears at the final path until the download is
// verified and unpacked. The models in m.Requires are fetched first, with
// the same options.
func Fetch(ctx context.Context, m Model, dir string, o FetchOptions) (string, error) {
	for _, id := range m.Requires {
		r, ok := Lookup(id)
		if !ok || r.ID == m.ID {
			return "", fmt.Errorf("model %s requires unknown model %s", m.ID, id)
		}
		if _, err := Fetch(ctx, r, dir, o); err != nil {
			return "", err
		}
	}
	dest := m.Path(dir)
	if o.Record != nil {
		if err := o.Record(dir, true); err != nil {
			return "", fmt.Errorf("record %s: %w", dir, err)
		}
		if err := o.Record(dest, false); err != nil {
			return "", fmt.Errorf("record %s: %w", dest, err)
		}
	}
	if m.Installed(dir) {
		return dest, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".download-"+m.ID+"-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	err = download(ctx, m, tmp, o)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if m.Archive == "" {
		if err := os.Chmod(tmp.Name(), 0o644); err != nil {
			return "", err
		}
		return dest, os.Rename(tmp.Name(), dest)
	}
	if m.Archive != "tar.bz2" {
		return "", fmt.Errorf("model %s: unsupported archive %q", m.ID, m.Archive)
	}
	xdir, err := os.MkdirTemp(dir, ".extract-"+m.ID+"-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(xdir) }()
	f, err := os.Open(tmp.Name())
	if err != nil {
		return "", err
	}
	err = untar(tar.NewReader(bzip2.NewReader(f)), xdir, 16*m.Size+(1<<20))
	_ = f.Close()
	if err != nil {
		return "", fmt.Errorf("model %s: unpack: %w", m.ID, err)
	}
	top := filepath.Join(xdir, m.File)
	if fi, err := os.Stat(top); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("model %s: archive has no %s folder", m.ID, m.File)
	}
	if err := os.Chmod(top, 0o755); err != nil {
		return "", err
	}
	return dest, os.Rename(top, dest)
}

func download(ctx context.Context, m Model, w io.Writer, o FetchOptions) error {
	client := o.Client
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.URL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("model %s: %w", m.ID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("model %s: %s: %s", m.ID, m.URL, resp.Status)
	}
	h := sha256.New()
	r := io.LimitReader(resp.Body, m.Size+1)
	if o.Progress != nil {
		r = &progressReader{r: r, total: m.Size, fn: o.Progress}
	}
	n, err := io.Copy(io.MultiWriter(w, h), r)
	if err != nil {
		return fmt.Errorf("model %s: download: %w", m.ID, err)
	}
	if n != m.Size {
		return fmt.Errorf("model %s: downloaded %d bytes, expected %d", m.ID, n, m.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != m.SHA256 {
		return fmt.Errorf("model %s: checksum mismatch: got sha256 %s, expected %s", m.ID, got, m.SHA256)
	}
	return nil
}

type progressReader struct {
	r           io.Reader
	done, total int64
	fn          func(done, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	p.fn(p.done, p.total)
	return n, err
}

// untar unpacks regular files and folders into dir, refusing anything that
// would land outside it, links, and more than limit bytes in total.
func untar(tr *tar.Reader, dir string, limit int64) error {
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if name == "." {
			continue
		}
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "\\") {
			return fmt.Errorf("unsafe path %q in archive", h.Name)
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			total += h.Size
			if h.Size < 0 || total > limit {
				return errors.New("archive is larger than expected")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, io.LimitReader(tr, h.Size))
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
		default:
			return fmt.Errorf("unsupported entry %q (type %c) in archive", h.Name, h.Typeflag)
		}
	}
}
