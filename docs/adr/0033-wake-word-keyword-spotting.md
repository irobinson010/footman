# 0033. Wake word: sherpa-onnx keyword spotting with a gated break-glass spotter

- Status: Accepted
- Date: 2026-11-26
- Amends: 0017, 0024

## Context
Issue #66 builds the production wake-word engine from spike S0-08 and the owner's live test (#8). The spike picked the sherpa-onnx keyword spotter (boost 2, threshold 0.05). Three things the spike did not settle: how keyword text becomes the model's pieces from Go, how the break-glass phrase of ADR-0017 reaches the pipeline, and what happens when the model is missing.

## Decision
- **Engine `sherpa` in `speech.WakeWords`** (native build). It takes 16 kHz audio in 100 ms chunks, and one detection resets the stream so an utterance fires once. The wake words are the listening personas' names plus "hey footman"; `speech.wake.boost` (default 2, 0.5 to 4) and `speech.wake.threshold` (default 0.05) tune it. Boost above 4 is refused by config, because the spike saw false wakes and a break-glass gate that fired on a bare phrase at 8.
- **Keywords are tokenised in Go.** The sherpa-onnx Go API takes pieces, not text, and the model ships only `bpe.model`. Despite the name it is a SentencePiece unigram model, so `internal/speech/sherpa/bpe.go` reads the pieces and scores from the protobuf and takes the best-scoring split (Viterbi). A test compares its output with the Python sentencepiece package for all the wake words. This avoids a Python step in setup and a new dependency.
- **Break-glass is a second spotter in the same engine, gated by the first.** After a wake word it listens for "stop everything" and "cancel everything" for 4 s, over the last 2 s of audio and onward, and reports `WakeEvent{Kind: WakeKindBreakGlass}`. It never runs, and so never fires, without a wake word (ADR-0017). `WakeEvent` gained only `Kind`; the zero value means a wake word, so existing engines and the fake are unchanged. The fake can emit either kind.
- **The pipeline stops on a break-glass event at once**, without waiting for the speech-to-text transcript of the utterance (the transcript check stays as the second path).
- **The model is optional at run time.** `footman setup` fetches `kws-zipformer-gigaspeech` (17 MB, SHA-256 pinned in the catalog) when `input.wake_word` is on. If it is missing when the daemon starts, the daemon logs it and runs with the hotkeys only, as it does without a VAD.

## Consequences
Detection on the synthetic clips matches the spike: "Alfred" 92%, "Hey Footman" 100%, no false wakes on 58 negative clips, about 2% of one core. Real rooms are weaker (owner notes: input level is the main risk), which belongs to the audio pipeline, not to these settings. Tests use 105 small clips in `testdata/wake` made with LibriTTS-R voices (CC BY 4.0). Persona wake words other than the persona's name, the `WakeWords` the router strips from transcripts, and automatic gain control are follow-ups.
