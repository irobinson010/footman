// Package speechtest holds helpers for the native speech engine tests: the
// test clip, where the models are, and a word error rate.
package speechtest

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/speech/models"
)

// ModelsEnv names the models folder for native tests. Tests that need a model
// are skipped when it is unset or the model is not in it.
const ModelsEnv = "FOOTMAN_TEST_MODELS"

// Reference is the transcript of Clip (LibriSpeech 1580-141084-0011).
const Reference = "WHEN WE WERE OUT IN THE DARKNESS OF THE QUADRANGLE WE AGAIN LOOKED UP AT THE WINDOWS"

// ClipPath returns the path of the 5-second test clip in testdata/speech.
func ClipPath(t testing.TB) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	p := filepath.Join(filepath.Dir(file), "..", "..", "..", "testdata", "speech", "librispeech-1580-141084-0011.wav")
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
	return p
}

// Clip loads the test clip as 16 kHz mono.
func Clip(t testing.TB) audio.Clip {
	t.Helper()
	c, err := audio.ReadWAVFile(ClipPath(t))
	if err != nil {
		t.Fatal(err)
	}
	return audio.ToSpeech(c)
}

// ModelsDir returns the models folder from $FOOTMAN_TEST_MODELS, skipping
// the test when it is unset or any of the named catalog models is missing.
func ModelsDir(t testing.TB, ids ...string) string {
	t.Helper()
	dir := os.Getenv(ModelsEnv)
	if dir == "" {
		t.Skipf("$%s is not set", ModelsEnv)
	}
	for _, id := range ids {
		m, ok := models.Lookup(id)
		if !ok {
			t.Fatalf("unknown model %s", id)
		}
		if !m.Installed(dir) {
			t.Skipf("model %s is not in %s (footman models fetch %s)", id, dir, id)
		}
	}
	return dir
}

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
	})
}

// WER is the word error rate of hyp against ref, ignoring case and
// punctuation.
func WER(ref, hyp string) float64 {
	r, h := words(ref), words(hyp)
	if len(r) == 0 {
		if len(h) == 0 {
			return 0
		}
		return 1
	}
	prev := make([]int, len(h)+1)
	cur := make([]int, len(h)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(r); i++ {
		cur[0] = i
		for j := 1; j <= len(h); j++ {
			sub := prev[j-1]
			if r[i-1] != h[j-1] {
				sub++
			}
			cur[j] = min(sub, prev[j]+1, cur[j-1]+1)
		}
		prev, cur = cur, prev
	}
	return float64(prev[len(h)]) / float64(len(r))
}
