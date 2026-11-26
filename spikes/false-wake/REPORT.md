# False-wake rate, offline (Sprint 2 exit check, #16)

**Result:** with the defaults (sherpa KWS zipformer gigaspeech, boost 2, threshold 0.05, AGC on), **0.24 false wakes per hour** over 8.30 h of audio with no wake words, so the < 1/h criterion is met. Both hits were "alfred". 95% upper bound ≈ 0.7/h (only 2 events).

**Update (#94):** the defaults are now boost 3 with a speech-to-text check of each wake, which gives 0 false wakes per hour on the same corpus and slightly better detection; see [Two-stage wake](#two-stage-wake-94-adr-0041).

Keywords in the run: ashford, alfred, hey footman. Audio fed directly (not played through a speaker and mic).

## By source

| Source | Hours | False wakes | Per hour | Keyword |
|---|---|---|---|---|
| Speech (LibriSpeech dev-other, CC BY 4.0) | 1.56 | 1 | 0.64 | alfred ("The officers were about to take leave…") |
| Radio (1930s–40s US broadcasts, public domain by age) | 3.31 | 0 | 0 | |
| Podcast (CC0 / PD mark) | 1.12 | 0 | 0 | |
| Music with vocals (PD-mark netlabel) | 1.45 | 0 | 0 | |
| Noise (aporee field recordings, PD mark: street, market, tram, crowd) | 0.87 | 1 | 1.15 | alfred (no speech) |
| **All** | **8.30** | **2** | **0.24** | |

Break-glass detections: 0.

## Sweep (AGC on)

Positives are the #74 synthetic clips (12 each).

| boost:threshold | false wakes | /h | Alfred | Hey Footman | stop everything (break-glass) | cancel everything (break-glass) |
|---|---|---|---|---|---|---|
| 2:0.05 (default) | 2 | 0.24 | 11/12 | 12/12 | 10/12 (10) | 9/12 (9) |
| 2:0.1 | 2 | 0.24 | 11/12 | 12/12 | 10/12 (10) | 9/12 (9) |
| 2:0.2 | 1 | 0.12 | 11/12 | 12/12 | 10/12 (10) | 9/12 (9) |
| 1.5:0.05 | 0 | 0 | 12/12 | 11/12 | 10/12 (10) | 8/12 (8) |
| 1.5:0.1 | 0 | 0 | 12/12 | 11/12 | 10/12 (10) | 8/12 (8) |
| 1:0.05 | 0 | 0 | 11/12 | 11/12 | 10/12 (10) | 8/12 (8) |

Looser, AGC off:

| boost:threshold | false wakes | /h | keywords | Alfred | Hey Footman |
|---|---|---|---|---|---|
| 2:0.05 | 2 | 0.24 | alfred 2 | 11/12 | 12/12 |
| 3:0.05 | 4 | 0.48 | alfred 3, ashford 1 | 12/12 | 12/12 |
| 4:0.05 | 16 | 1.93 | alfred 14, ashford 2 | 12/12 | 12/12 |

- Threshold 0.05–0.2 changes almost nothing; boost is the knob. Boost 4 fails the criterion.
- AGC on/off give identical hits at 2:0.05.
- Differences between 1.5 and 2 are one clip on 12-clip sets: noise.

## Recommendation

For the spotter on its own (`speech.wake.verify` off): keep boost 2, threshold 0.05. If a real room is worse, lower `speech.wake.boost` to 1.5 first. Don't loosen past 2 on the spotter alone; the owner's live test (misses at natural pace) is addressed by #94 instead: a looser spotter (boost 3 gives 0.48/h here) plus an STT check that the transcript starts with a wake word.

## Two-stage wake (#94, ADR-0041)

`TestWakeVerify` (`internal/speech/sherpa/wakeverify_test.go`) runs every file through the real pipeline (`core.Run` with a fake microphone and desktop): AGC, spotter and Silero VAD, the command cut, Parakeet TDT 0.6B v2 int8 **on the CPU**, and with verification on, the wake-word check of the transcript. "Acted" means the turn was routed to a command or dictation. Break-glass events from the spotter are counted (none in any run) and not passed on.

**Result:** with the check, **0 false wakes per hour** at boost 3 and at boost 4 (95% upper bound about 0.36/h, 0 events in 8.3 h). Every spotter hit was rejected: 4 for `no_wake_word` and 1 for `no_speech` at boost 3, 12 and 1 at boost 4. Detection at a natural pace goes from 98 to 101 of 120 clips. New defaults: `speech.wake.boost` 3, `speech.wake.verify` on.

### False wakes on the corpus (8.30 h, AGC on)

| boost | check | spotter hits (/h) | acted on (/h) |
|---|---|---|---|
| 2 | off (old default) | 2 (0.24) | 2 (0.24) |
| 3 | off | 5 (0.60) | 5 (0.60) |
| **3** | **on (new default)** | 5 (0.60) | **0** |
| 4 | off | 14 (1.69) | 12 (1.45) |
| 4 | on | 14 (1.69) | **0** |

All hits were "alfred". By source at boost 4: podcast 5.36/h, noise 2.31/h, speech 1.28/h, radio 1.21/h, music 0. What the check heard is in `verify.tsv`: ordinary speech ("Sometimes I think", "The officers were about to take leave…", "No, Fred. What about Porsche?") or nothing (street noise); none starts with anything close to a wake word.

### Detection

Natural-pace clips (`genpos.py`, 20 LibriTTS-R voices not used by `testdata/wake`, brisk and normal pace, wake word and command in one breath: "Ashford, open YouTube.", "Alfred, what time is it?", "Hey Footman, pause the music."; 40 per wake word):

| boost | check | Ashford | Alfred | Hey Footman | total |
|---|---|---|---|---|---|
| 2 | off (old default) | 24 | 39 | 35 | 98 / 120 |
| 3 | off | 25 | 39 | 36 | 100 / 120 |
| **3** | **on (new default)** | 23 | 38 | 40 | **101 / 120** |
| 4 | on | 24 | 38 | 40 | 102 / 120 |

- **Ashford is the spotter's weak word**: it fires on 24–26 of 40 at any boost, so boost barely helps it. The check then loses 2 more whose transcripts are far from the name ("That's for it open YouTube", "Astrid Open U2", "Effort open YouTube"). The voices may say the name oddly; the owner's voice needs a live test. A spotter-side fix (keyword variants, a bigger model) is a follow-up.
- **The check fixes a cut bug for "Hey Footman"**: without it, 4 clips reached speech-to-text with nothing but silence after the 600 ms pre-roll (empty transcript, nothing done), because the spotter reported the end of the wake word late. With the check the audio starts at the beginning of the segment, so all 40 work.
- The one Alfred clip lost to the check was heard as "Alpha".

The #74 synthetic clips, with a command clip after a 150 ms or 1.2 s pause (the wake word alone, then the command), pass the check: Alfred 11/12 and Hey Footman 12/12 at boosts 3 and 4, with or without the check (boost 2: 12/12 for both; the Alfred clip missed at 3 and 4 is missed by the spotter, so it is one clip of noise).

### Cost

Each wake transcribes the wake word too: on the natural-pace clips 1.37 s of audio instead of 1.00 s, 67 ms instead of 55 ms with Parakeet on the CPU (about **+12 ms**); on the synthetic clips with a longer command, about +25 ms (3.3 s instead of 2.7 s). A rejected wake costs one transcription that was not made before; on the corpus these averaged 0.6 s of CPU at boost 3 (1–2 per hour), more for hits in long segments (up to 29 s of audio, 1.9 s). The GPU engine is faster.

### Rerun

```bash
D=~/PersonalProjects/footman-data/false-wake
uv run --no-project --with sherpa-onnx==1.13.8 --with numpy --with scipy --with soundfile \
  python -I spikes/false-wake/genpos.py PIPER_DIR $D/positives   # once; PIPER_DIR as for testdata/wake/gen.py
CUDA_VISIBLE_DEVICES= FOOTMAN_TEST_MODELS=~/.local/share/footman/models FOOTMAN_FALSE_WAKE_DIR=$D \
  FOOTMAN_FALSE_WAKE_WORKERS=12 FOOTMAN_WAKE_VERIFY_SETTINGS="2:0,3:0,3:1,4:0,4:1" \
  go test -tags native -run TestWakeVerify -count=1 -timeout 3h -v ./internal/speech/sherpa/
```
About 10 minutes for these five settings. The models folder needs the keyword spotter, Silero VAD and Parakeet.

## Limits

- Only 8.3 h and 2 events.
- No modern TV, kitchen or typing noise; the radio is old and band-limited; the noise is mostly street ambience.
- Audio was fed directly, not through a speaker and mic.
- Positive clips are synthetic, and "ashford" has none, so its detection is unmeasured.
- The old-time-radio items have no licence field on archive.org; public domain by age is the uploader's claim.

## Rerun

```bash
D=~/PersonalProjects/footman-data/false-wake
FOOTMAN_TEST_MODELS=$D/models FOOTMAN_FALSE_WAKE_DIR=$D FOOTMAN_FALSE_WAKE_SETTINGS="2:0.05,1.5:0.05" \
  go test -tags native -run TestFalseWake -count=1 -v ./internal/speech/sherpa/
```
The full six-setting sweep takes about 8 minutes. Audio, downloads and the model copy are in the data dir, outside the repo. `fetch.py` rebuilds the corpus; `transcribe.py` transcribes around each hit.
