# False-wake rate, offline (Sprint 2 exit check, #16)

**Result:** with the defaults (sherpa KWS zipformer gigaspeech, boost 2, threshold 0.05, AGC on), **0.24 false wakes per hour** over 8.30 h of audio with no wake words, so the < 1/h criterion is met. Both hits were "alfred". 95% upper bound ≈ 0.7/h (only 2 events).

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

Keep boost 2, threshold 0.05. If a real room is worse, lower `speech.wake.boost` to 1.5 first. Don't loosen past 2 on the spotter alone; the owner's live test (misses at natural pace) is addressed by #94 instead: a looser spotter (boost 3 gives 0.48/h here) plus an STT check that the transcript starts with a wake word.

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
