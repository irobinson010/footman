# 0037. Interim TTS: sherpa-onnx Kokoro and Piper voices, phonemized in Go

- Status: Accepted
- Date: 2026-11-26
- Amends: 0013, 0019

## Context
Sprint 3 needs a local voice now, but the bake-off's small model (#10, ADR-0013) can't be trained until the GPU is free. Issue #79 asks for an interim engine behind `speech.TTS` that loads both bake-off candidate families, Kokoro (StyleTTS2) and Piper (VITS), picks stock English voices against the design §31 pass bar on the CPU, and lets the trained Ashford model replace the stock voice by changing only its model path.

What we found on the way: **sherpa-onnx 2.x (our pinned v2.0.3) no longer links espeak-ng.** Its TTS models ignore `data_dir`, and the library has no espeak symbols. Text goes in through a pronunciation lexicon (an unknown word is skipped silently) or as phoneme codepoints that you make yourself. Kokoro's own British lexicon has no "Ashford" or "Pettigrew", and both lexicons miss words such as "tidied" or "API". With the stock lexicons alone, Footman would drop the persona's own name.

## Decision
**Engine.** `internal/speech/sherpa` registers `kokoro` and `piper` in `speech.TTSs` (native build tag, the same sherpa-onnx wrapper as the speech-to-text and wake-word engines, ADR-0027). Both names are one engine, `sherpa.TTS`. The name picks only the default model (`kokoro-en` or `piper-en-us-norman`). The model's files decide how it runs: a folder with `voices.bin` is Kokoro; an `.onnx` with `tokens.txt` is VITS/Piper. Each model asked for stays loaded until `Close`, which also stops syntheses in progress. Idle unloading (`cmd/footman/unload.go`, ADR-0028) wraps it unchanged; a native test unloads and reloads it.

**Voice contract.** `speech.Voice` gained `Speaker`, and its fields map one-to-one onto a persona pack's `[voice]` section (agreed with #78):
- `ID` is `model`: a models-catalog ID (`kokoro-en`) or an absolute path to a model folder or `.onnx` file. The persona loader resolves pack-relative paths (`voice/ashford.onnx`) against the pack folder. Empty means the engine's default.
- `Speaker` is `speaker`: a name (`bm_george`, read from the model's `speaker_names` metadata or a Piper `.onnx.json` `speaker_id_map`) or an index. A single-speaker model ignores it, so a pack that swaps a stock voice for its own fine-tuned model keeps working unchanged.
- `Language` (`en-GB`/`en-US`) picks the accent; otherwise a Kokoro speaker's prefix does (`b…` British, `a…` American). `Speed` is `speed`.

**Footman phonemizes.** The engine passes sherpa-onnx phoneme codepoints, never text (`tts_g2p.go`):
1. The model's own lexicon in its own alphabet: Kokoro's `lexicon-gb-en.txt` or `lexicon-us-en.txt` (misaki phonemes), or a `lexicon*.txt` in a Piper folder.
2. Then the shared `tts-lexicon-en-us` (sherpa-onnx's `lexicon-en-us.txt`, 168k words in espeak-ng IPA, catalog entry, 6 MB). For Kokoro it is converted to misaki symbols, following misaki's own espeak fallback, with the r dropped for British voices. This is how Kokoro says "Ashford", "Klaus" and "Pettigrew".
3. Then possessives, plurals, `-ed`/`-ied`/`-ing`/`-ly` of known words, CamelCase and two-word compounds. Short capitals (`API`, `BBC`) and anything still unknown are spelled letter by letter, so no word is dropped silently.
Only codepoints in the model's `tokens.txt` are sent. The lexicons take about 40 MB of Go heap.

**Text front end** (`NormalizeText`, exported for the clip bank): it spells out times (`7:45`, `9:05 pm`, `14:30`), money (`£42.50`, `$1`, `€2.5m`), percentages, ordinals, years, ISO dates, grouped numbers, decimals, digit strings, version numbers, ranges, simple fractions, units after numbers (`16GB`, `120 ms`, `20°C`), common abbreviations (`Mr.`, `Dr.`, `etc.`, `e.g.`), symbols, URLs and e-mail addresses, and strips Markdown emphasis. Numbers are read British style ("one hundred and five").

**Streaming.** `Speak` returns at once with an `audio.Stream` in the model's own rate (Kokoro 24 kHz, Piper 22.05 kHz, mono float32), which `platform.Audio.Play` takes as is. The text is cut into sentences, and the first sentence is cut again at its first comma after 4 words, so the first audio comes sooner. Sentences over 25 words are cut at pauses. Each piece is synthesized in turn and its audio goes on the stream in 20 ms frames as soon as it is ready, with 0.15 s between sentences. Synthesis never waits for the reader. Cancelling ctx or closing the stream stops it after the piece in progress (sherpa-onnx cannot stop one midway); `Close` waits for it, so the engine is idle afterwards. Syntheses on one engine run one at a time.

**Models** (catalog, `Kind: "tts"`, SHA-256 pinned; each voice `Requires` the lexicon, which `models.Fetch` now fetches first):

| ID | Download | Weights / data license | Notes |
|---|---|---|---|
| `kokoro-en` | 350 MB (`model.onnx` 326 MB) | Apache-2.0 (Kokoro-82M v1.0) | 54 speakers, 9 of them English; the default |
| `piper-en-us-norman` | 67 MB | Piper voices repo (MIT); data LibriVox, public domain | light fallback, male; ADR-0019 lists it as a clean base |
| `piper-en-us-ljspeech` | 67 MB | Piper voices repo (MIT); data LJ Speech, public domain | light fallback, female; clean base |
| `tts-lexicon-en-us` | 6 MB | **unconfirmed**: CMUdict (BSD-style) plus espeak-ng's `en_list` (GPL-3.0), phonemes probably made with espeak-ng | see open items |

The **fp32** Kokoro is the interim default because int8 does not hold on this CPU: same voice, but 4× slower (below). Likewise the Piper int8 builds were 5× slower than fp32 and were left out. British Piper voices were left out too: the only lexicon for Piper is American, and the British stock voices' datasets (`alan`, `alba`, `jenny`…) are not on ADR-0019's clean list.

**Interim default voice:** `kokoro` engine, `kokoro-en`, speaker `bm_george`. Proposed per persona, for the owner to confirm by ear in #84 (samples below): Ashford `bm_george`, Klaus `bm_lewis` (deeper; Kokoro has no German-accented English voice, `am_fenrir` is the alternative), Pettigrew `bm_fable`, Lucy `bf_emma` (or `af_heart`, Kokoro's best-rated voice, if an American Lucy is fine).

## Measurements (S3-02)
Ryzen 9 7900X, CPU only, 8 threads, while other work kept the machine's load average at 8–9. Medians of 3 timed runs after a warm-up, via `TestTTSBench` (one process per voice). The time to first audio runs from `Speak` to the first frame. RTF is total synthesis time divided by audio length. RSS is the growth of the process after loading and after synthesis. Intelligibility: Parakeet transcribed the long sample, and the word error rate is taken after both sides go through `NormalizeText`. Most of the remaining errors are "Dr" against "Doctor" and "7.45" against "seven forty-five".

Sentences: short "Very good, sir. The kettle is on." (2–3 s); long, 31 words with a time, an ordinal, money and "Dr. Pettigrew" (12–15 s).

| Voice | First audio short / long | RTF short / long | RSS loaded → after | ASR WER |
|---|---|---|---|---|
| kokoro-en bm_george | 236 / 277 ms | 0.169 / 0.151 | +458 → +735 MB | 5% |
| kokoro-en bm_fable | 228 / 291 ms | 0.144 / 0.127 | +462 → +732 MB | 7% |
| kokoro-en bm_lewis | 241 / 299 ms | 0.180 / 0.147 | +460 → +733 MB | 5% |
| kokoro-en bm_daniel | 215 / 259 ms | 0.158 / 0.134 | +460 → +723 MB | 5% |
| kokoro-en bf_emma | 223 / 296 ms | 0.177 / 0.146 | +456 → +733 MB | 7% |
| kokoro-en bf_isabella | 266 / 292 ms | 0.200 / 0.144 | +461 → +733 MB | 7% |
| kokoro-en af_heart | 210 / 245 ms | 0.193 / 0.139 | +457 → +732 MB | 5% |
| kokoro-en am_michael | 237 / 292 ms | 0.194 / 0.148 | +461 → +876 MB | 5% |
| kokoro-en am_fenrir | 215 / 237 ms | 0.197 / 0.151 | +455 → +743 MB | 7% |
| piper-en-us-norman | 29 / 41 ms | 0.025 / 0.023 | +127 → +352 MB | 14% |
| piper-en-us-ljspeech | 30 / 39 ms | 0.026 / 0.022 | +125 → +407 MB | 11% |
| *Kokoro int8 (not shipped), bm_george* | *950 / 1226 ms* | *0.706 / 0.573* | *+256 → +631 MB* | |
| *Piper norman int8 (not shipped)* | *161 / 261 ms* | *0.153 / 0.155* | *+90 → +293 MB* | *14%* |

**Against the §31 pass bar** (CPU: first audio < 600 ms, RTF < 0.3, model ≤ ~350 MB), every shipped voice passes: Kokoro with roughly 2× headroom, Piper with 10×. Kokoro's 326 MB model is at the size limit. Kokoro int8 fails both speed targets.

Threads (Kokoro bm_george, short / long first audio, long RTF): 4 threads 287 / 357 ms, 0.196; **8 threads 236 / 277 ms, 0.151**; 12 threads 272 / 335 ms, 0.160. The default is `min(8, CPUs/2)`.

Memory: Kokoro's onnxruntime arena grows with the longest piece it has synthesized, which is why sentences are capped at 25 words. A Kokoro engine holds about 0.7 GB, and idle unloading (`speech.unload_after`) returns it.

The samples for the owner are in `~/PersonalProjects/footman-data/tts-interim/` (`listen.html`; not in git): nine Kokoro voices and two Piper voices, each saying a persona line and the long sentence, plus the int8 Kokoro for comparison.

## How the trained model replaces it
The trained Ashford voice ships as a model folder (or an `.onnx` with its files beside it) in sherpa-onnx's layout. For Kokoro that is `model.onnx` with sherpa-onnx's metadata (`add_meta_data.py`), a one-speaker `voices.bin`, `tokens.txt` and, ideally, the lexicons. For Piper it is the `.onnx` with sherpa-onnx's metadata, `tokens.txt` made from the `.onnx.json` phoneme map, and the `.onnx.json`. The persona pack's `[voice] model` then points at it (`voice/ashford.onnx` or a catalog ID once it is published). `speaker` can stay or go. No code changes: the engine works out the family from the files, finds no speaker names on a single-speaker model, and uses the same phonemizer. The export step belongs in the bake-off training kit (#10).

## Consequences
- A sentence synthesizes on the CPU through `speech.TTSs` with the interim default, and every voice passes the §31 speed bar. The < 300 ms acknowledgement in the sprint exit still comes from the clip bank (#80). Live Kokoro is at about 240 ms for a short line here, but not with margin on slower machines.
- **Licensing, open items for the owner (not resolved here):**
  - (a) **espeak-ng (GPL-3.0), ADR-0019 item (b), changes shape.** Footman's binary no longer links espeak-ng code (sherpa-onnx 2.x dropped it). The GPL question moves to **data**: `tts-lexicon-en-us` lists espeak-ng's `en_list` as a source, and its phonemes were probably generated with espeak-ng. Two more points: the Kokoro and Piper archives still unpack an unused `espeak-ng-data/` folder (19 MB, GPL-3.0 data) into the models folder, and the converted pronunciations reach Kokoro. Under ADR-0020 none of this blocks development. Before a release ships voices or a lexicon, decide whether a downloaded GPL-derived lexicon is acceptable, or replace it with a CMUdict-only lexicon (BSD) plus a permissive grapheme-to-phoneme model.
  - (b) The Kokoro lexicons (`lexicon-gb-en.txt`, `lexicon-us-en.txt`) are taken to come from misaki's dictionaries (Apache-2.0); not verified.
  - (c) The Piper voices' weights carry the piper-voices repository's MIT tag. ADR-0019 notes that this tag grants no rights in the data. Norman and LJ Speech are public-domain recordings.
- `footman models fetch` with no arguments does not yet fetch the default persona's voice, and the daemon builds `speech.tts.engine` without options (so no `models_dir`). Both are #84's wiring in `cmd/footman`. The engine and catalog are ready: `footman models fetch kokoro-en` fetches the voice and its lexicon.
- Known front-end limits: no general letter-to-sound rules, so an unknown long word is spelled; British "Dr" and similar are expanded only with the full stop; numbers are always read as quantities (a "4080" model number is "four thousand and eighty"). Heteronyms (read/read, live/live) take the lexicon's first entry.
- The bench (`FOOTMAN_TTS_BENCH=<dir> FOOTMAN_TTS_BENCH_CASES=kokoro-en@bm_george,… go test -tags native -run TestTTSBench ./internal/speech/sherpa/`) can be re-run on the ZenBook (#7) and when the trained model arrives.
