//go:build native && cgo

package sherpa

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/registry"
	"github.com/irobinson010/footman/internal/speech"
	"github.com/irobinson010/footman/internal/speech/speechtest"
)

const kwsModel = "kws-zipformer-gigaspeech"

// wakeClips is the synthetic clip set (testdata/wake, made by gen.py) and
// two LibriSpeech clips of ordinary speech.
func wakeClips(t *testing.T, prefix string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join("..", "..", "..", "testdata", "wake", prefix+"_s[0-9]*.wav"))
	if err != nil || len(m) == 0 {
		t.Fatalf("no testdata/wake/%s_s[0-9]*.wav clips", prefix)
	}
	return m
}

func loadClip(t *testing.T, path string) audio.Clip {
	t.Helper()
	c, err := audio.ReadWAVFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return audio.ToSpeech(c)
}

// listen streams a clip through Listen in 20 ms frames, as the microphone
// would deliver it, and returns the events.
func listen(t *testing.T, k *KWS, c audio.Clip) []speech.WakeEvent {
	t.Helper()
	frames := make(chan speech.Frame)
	go func() {
		defer close(frames)
		for _, f := range c.Frames(320) {
			frames <- f
		}
	}()
	var evs []speech.WakeEvent
	for e := range k.Listen(context.Background(), frames) {
		evs = append(evs, e)
	}
	return evs
}

func count(evs []speech.WakeEvent, kind speech.WakeKind, keyword string) int {
	n := 0
	for _, e := range evs {
		if e.Kind == kind && (keyword == "" || e.Keyword == keyword) {
			n++
		}
	}
	return n
}

func openKWS(t *testing.T) *KWS {
	t.Helper()
	dir := speechtest.ModelsDir(t, kwsModel)
	k, err := OpenKWS(KWSOptions{Common: Common{ModelsDir: dir}, Keywords: []string{"Alfred", "hey footman"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k.Close() })
	t.Log(k.Info())
	return k
}

func TestKWSRegistered(t *testing.T) {
	if !speech.WakeWords.Has(KWSName) {
		t.Fatalf("%s is not registered", KWSName)
	}
	dir := speechtest.ModelsDir(t, kwsModel)
	w, err := speech.WakeWords.New(KWSName, registry.Map{"models_dir": dir, "keywords": []string{"ashford"}, "boost": 2, "threshold": 0.05})
	if err != nil {
		t.Fatal(err)
	}
	_ = w.(*KWS).Close()
}

func TestKWSTokenizesLikeSentencePiece(t *testing.T) {
	dir := speechtest.ModelsDir(t, kwsModel)
	b, err := loadBPE(filepath.Join(dir, "sherpa-onnx-kws-zipformer-gigaspeech-3.3M-2024-01-01", "bpe.model"))
	if err != nil {
		t.Fatal(err)
	}
	// Expected strings are from the sentencepiece Python package.
	for phrase, want := range map[string]string{
		"alfred":            "▁A L F RE D",
		"hey footman":       "▁HE Y ▁F O O T MAN",
		"ashford":           "▁A SH F OR D",
		"klaus":             "▁K LA US",
		"pettigrew":         "▁P ET T IG RE W",
		"lucy":              "▁ LU C Y",
		"stop everything":   "▁ST O P ▁EVERYTHING",
		"cancel everything": "▁CAN CE L ▁EVERYTHING",
		"hello world":       "▁HE LL O ▁WORLD",
		"merry christmas":   "▁ME R RY ▁ CH R IST MA S",
		"jarvis":            "▁JA R VI S",
	} {
		got, err := b.encode(phrase)
		if err != nil || strings.Join(got, " ") != want {
			t.Errorf("%q = %q, %v; want %q", phrase, got, err, want)
		}
	}
}

func TestKWSDetection(t *testing.T) {
	k := openKWS(t)

	var chunks int
	var cpu time.Duration
	run := func(c audio.Clip) []speech.WakeEvent {
		start := time.Now()
		evs := listen(t, k, c)
		cpu += time.Since(start)
		chunks += (len(c.Samples) + chunkSamples - 1) / chunkSamples
		return evs
	}

	// Positives: the wake word must be heard once, not twice.
	for _, tc := range []struct {
		prefix, keyword string
		min             float64
	}{{"alfred", "alfred", 0.9}, {"hey_footman", "hey footman", 0.75}} {
		clips := wakeClips(t, tc.prefix)
		hit, twice := 0, 0
		for _, p := range clips {
			n := count(run(loadClip(t, p)), speech.WakeKindWake, tc.keyword)
			if n > 0 {
				hit++
			}
			if n > 1 {
				twice++
			}
		}
		rate := float64(hit) / float64(len(clips))
		t.Logf("%q: woke on %d of %d clips (%.0f%%), %d fired twice", tc.keyword, hit, len(clips), 100*rate, twice)
		if rate < tc.min {
			t.Errorf("%q woke on %.0f%% of clips, want at least %.0f%%", tc.keyword, 100*rate, 100*tc.min)
		}
		if twice > 0 {
			t.Errorf("%q fired twice on %d clips", tc.keyword, twice)
		}
	}

	// Negatives: other names, ordinary speech, the stop phrases without the
	// wake word and silence must not wake it at all.
	var negatives []string
	for _, p := range []string{"jarvis", "alexa", "hey_mycroft", "talk1", "talk2", "stop", "cancel"} {
		negatives = append(negatives, wakeClips(t, p)...)
	}
	negatives = append(negatives, speechtest.ClipPath(t), speechtest.Clip10Path(t))
	for _, p := range negatives {
		if evs := run(loadClip(t, p)); len(evs) > 0 {
			t.Errorf("%s: false events %+v", filepath.Base(p), evs)
		}
	}
	if evs := run(audio.Clip{Format: audio.Speech, Samples: make([]float32, 10*16000)}); len(evs) > 0 {
		t.Errorf("silence: events %+v", evs)
	}
	t.Logf("%d negative clips, no false events", len(negatives))

	// Break-glass: after the wake word, once; never without it.
	for _, tc := range []struct{ prefix, phrase string }{{"alfred_stop", "stop everything"}, {"alfred_cancel", "cancel everything"}} {
		clips := wakeClips(t, tc.prefix)
		woke, bg, noWake, wrong, twice := 0, 0, 0, 0, 0
		for _, p := range clips {
			evs := run(loadClip(t, p))
			w := count(evs, speech.WakeKindWake, "alfred") > 0
			n := count(evs, speech.WakeKindBreakGlass, tc.phrase)
			if w {
				woke++
			}
			if w && n > 0 {
				bg++
			}
			if !w && n > 0 {
				noWake++
			}
			if n > 1 {
				twice++
			}
			wrong += count(evs, speech.WakeKindBreakGlass, "") - n
		}
		t.Logf("Alfred, %s: woke on %d of %d clips, then break-glass on %d of those", tc.phrase, woke, len(clips), bg)
		if float64(woke) < 0.75*float64(len(clips)) {
			t.Errorf("woke on only %d of %d clips", woke, len(clips))
		}
		if float64(bg) < 0.9*float64(woke) {
			t.Errorf("break-glass %q fired on %d of %d clips that woke, want at least 90%%", tc.phrase, bg, woke)
		}
		if noWake > 0 {
			t.Errorf("break-glass %q fired on %d clips without the wake word", tc.phrase, noWake)
		}
		if wrong > 0 || twice > 0 {
			t.Errorf("break-glass %q: %d wrong phrases, %d fired twice", tc.phrase, wrong, twice)
		}
	}

	per := cpu / time.Duration(chunks)
	t.Logf("%d chunks of 100 ms in %s of wall time: %s per chunk on average (includes the test's frame handling)", chunks, cpu.Round(time.Millisecond), per.Round(time.Microsecond))
	if per > 50*time.Millisecond {
		t.Errorf("%s per 100 ms chunk is too slow for real time", per)
	}
}

// A break-glass phrase later than the window after the wake word is ignored,
// and the stop phrase on its own never fires, even right after other speech.
func TestKWSBreakGlassNeedsWake(t *testing.T) {
	k := openKWS(t)
	stop := loadClip(t, wakeClips(t, "stop")[0])
	alfred := loadClip(t, wakeClips(t, "alfred")[0])
	gap := func(sec int) []float32 { return make([]float32, sec*16000) }

	join := func(parts ...[]float32) audio.Clip {
		var s []float32
		for _, p := range parts {
			s = append(s, p...)
		}
		return audio.Clip{Format: audio.Speech, Samples: s}
	}
	// "stop everything" first, then "Alfred": the stop phrase came before
	// the wake word, so it does not count.
	if evs := listen(t, k, join(stop.Samples, gap(1), alfred.Samples)); count(evs, speech.WakeKindBreakGlass, "") != 0 {
		t.Errorf("break-glass before the wake word fired: %+v", evs)
	}
	// "Alfred", then the stop phrase after the 4 s window has closed.
	evs := listen(t, k, join(alfred.Samples, gap(6), stop.Samples))
	if count(evs, speech.WakeKindBreakGlass, "") != 0 {
		t.Errorf("break-glass after the window fired: %+v", evs)
	}
}

func TestKWSOffsetsAndClose(t *testing.T) {
	k := openKWS(t)
	c := loadClip(t, wakeClips(t, "alfred")[0])
	frames := c.Frames(1600)
	for i := range frames {
		frames[i].Offset += time.Minute
	}
	ch := make(chan speech.Frame, len(frames))
	for _, f := range frames {
		ch <- f
	}
	close(ch)
	var at time.Duration
	for e := range k.Listen(context.Background(), ch) {
		if e.Kind == speech.WakeKindWake {
			at = e.At
		}
	}
	if end := time.Minute + c.Duration(); at < time.Minute || at > end+2*time.Second {
		t.Errorf("event at %s, clip spans %s to %s", at, time.Minute, end)
	}
	if err := k.Close(); err != nil {
		t.Fatal(err)
	}
	if _, open := <-k.Listen(context.Background(), make(chan speech.Frame)); open {
		t.Error("Listen after Close sent an event")
	}
}

func TestKWSRejects(t *testing.T) {
	dir := speechtest.ModelsDir(t, kwsModel)
	for _, o := range []KWSOptions{
		{Common: Common{ModelsDir: dir}, Keywords: []string{"café"}},
		{Common: Common{ModelsDir: dir}, Boost: 20},
		{Common: Common{ModelsDir: t.TempDir()}},
	} {
		if k, err := OpenKWS(o); err == nil {
			_ = k.Close()
			t.Errorf("%+v accepted", o)
		}
	}
}
