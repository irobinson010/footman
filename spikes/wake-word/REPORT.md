# S0-08 spike: wake word, VAD and break-glass phrase

Status: **automated part done; owner live test pending** (section 7). Refs #8.

Everything here ran on the CPU (1 thread per engine, 2 jobs at most), with no GPU and no microphone. All audio is synthetic or from public corpora, so **these numbers show pipeline behaviour, not what a real room will do**. See section 4.

## 1. Summary and provisional recommendation

| Question | Answer so far |
|---|---|
| Engine | **sherpa-onnx keyword spotting** (English zipformer, int8) for the product wake words. It spots any text phrase with no training (Alfred, Jarvis, Hey Footman all work), has official Go bindings, and costs about 1.1% of one core. |
| Default sensitivity | `keywords_score` (boost) **2.0**, `keywords_threshold` **0.05**. Boost is the real sensitivity knob; threshold barely matters. Expose one "sensitivity" setting that maps to boost 1 (strict) to 4 (loose). Do not go past 4: boost 8 gave 12 false ALFRED per hour and made the break-glass gate fire on bare "cancel everything". |
| False wakes | On the 61-minute hour: **0 for "alfred" at every setting up to boost 4**, 0-1 per hour across Alfred / Jarvis / Hey Footman (one "jarvis" at boost 2, none at boost 1 or 4). 1 hour only bounds the rate to roughly 3 per hour at 95% confidence (rule of three), so **< 1 per hour looks reachable but is not proven**; plan in section 6. |
| Wake latency | The word is reported **about 0.4 s after it ends** for "Alfred" (mean; p95 0.6-0.7 s). Two-word phrases are quicker (about 0.3 s). |
| Break-glass (ADR-0017) | Two-stage works. After "Alfred", "stop everything" / "cancel everything" is spotted 100% of the time in clean audio (88-100% at 20 dB SNR, 75-92% at 10 dB), and the gated fire follows the end of speech by 0.05-0.15 s. "Stop everything" / "cancel everything" **without** "Alfred" fired the break-glass **0 times in 240 clips** at every setting up to boost 4. The gate is essential: the stage-2 spotter alone hears the bare phrase 75-100% of the time in 10-20 dB audio. |
| VAD | Silero v5 via sherpa-onnx: 0.23% of a core, 99-100% utterance recall, onset about 0.2 s. Good as an idle-CPU gate and endpointer; it cannot tell the owner from the TV (section 3.5). |
| openWakeWord | Better latency and strong on its own names, but **no Alfred / Footman model** and Python-only. Keep it as a comparison and as the target of the later training kit (design section 23). |

## 2. What is available out of the box

**sherpa-onnx KWS** (`k2-fsa/sherpa-onnx`, release `kws-models`): open vocabulary. You give it keyword text, tokenised with the model's BPE (`py/make_keywords.py`), with an optional per-keyword boost and threshold. Models: `kws-zipformer-gigaspeech-3.3M-2024-01-01` (English, used here; int8 encoder 4.6 MB), `kws-zipformer-wenetspeech-3.3M-2024-01-01` (Chinese), `kws-zipformer-zh-en-3M-2025-12-20` (bilingual). Names tested: alfred, jarvis, hey footman, hey jarvis, alexa, hey mycroft. Open vocabulary does not mean equally good for every word (section 3.2: "hey mycroft" 0%, "alexa" 38%).

**openWakeWord** (pip `openwakeword` 0.4.0, all ONNX and bundled in the package): `alexa`, `hey_jarvis`, `hey_mycroft`, `hey_marvin`, plus the command models `timer` and `weather`. (`hey_rhasspy` is not in this release's package.) Shared front end: `melspectrogram.onnx` and `embedding_model.onnx`; each wake word is a 0.5-1.3 MB classifier. Tested: alexa, hey_jarvis, hey_mycroft. There is **no "alfred" or "hey footman"** model; those need the custom training kit.

**Go path.** sherpa-onnx runs through the official `github.com/k2-fsa/sherpa-onnx-go-linux` bindings. The spike has its own `go.mod`, so the repo root is untouched. openWakeWord has no practical Go path: it is a three-stage chain (mel, embedding, classifier) that needs a generic ONNX runtime binding. For the spike it runs as a Python helper (`py/oww_scores.py`) that writes per-frame scores; the Go harness does the thresholding and scoring. A port to onnxruntime-go (three small models) is possible later if OWW wins.

## 3. Measurements

Test material (all gitignored; recreate with `./setup.sh`):

- **Background hour (61.0 min):** 20 min LibriSpeech dev-clean read speech (CC BY 4.0), 13 min AMI meeting speech, IHM channel (CC BY 4.0; spontaneous multi-speaker conversation), 15 min music, and 13 min "TV" (LibriSpeech over music at 11 dB SNR). Music is 70 Free Music Archive `fma_small` tracks of 30 s, 8 genres, Creative Commons only (53 are CC BY-NC-ND, 17 CC BY; used locally for research, not redistributed). Blocks are shuffled in 2-minute pieces.
- **Positive clips (1200):** synthesised on CPU with Piper/VITS voices through sherpa-onnx TTS: 10 LibriTTS-R speakers plus `ryan` and `amy` (12 voices), 2 speaking rates, 10 phrases: Alfred, Jarvis, Hey Footman, Hey Jarvis, Alexa, Hey Mycroft, "Alfred, stop everything", "Alfred, cancel everything", and without the wake word "Stop everything" and "Cancel everything". Each is placed in an 8 s excerpt of the background hour at **clean / 20 / 10 / 5 / 0 dB SNR** (speech level vs the excerpt). That is 24 clips per phrase per SNR.
- **Sensitivity settings (sherpa):** boost/threshold pairs `1/0.25` (library default), `1/0.1`, `2/0.05`, `4/0.02`, `8/0.01`. openWakeWord: thresholds 0.3, 0.5 (its default), 0.7, 0.9, with a 2 s lockout after each wake.
- **Streaming:** 100 ms chunks (sherpa) and 80 ms frames (OWW), as a live pipeline would see them. Detection time is the audio time at which the engine reports. "Latency" is that minus the end of the word, where "end" is the -40 dB trimmed end of the synthesised speech (so it is a little pessimistic for an early-firing model). For the longer break-glass phrases the end of "Alfred" is estimated from the same voice saying "Alfred" alone.

Raw tables are written to `data/results/` by `./run_all.sh` (not committed). The ones that matter are below.

### 3.1 False wakes on the 61-minute background hour

| sherpa-onnx, boost / threshold | alfred | jarvis | hey footman | other test names | product names per hour | CPU, % of 1 core |
|---|---|---|---|---|---|---|
| 1 / 0.25 (default) | 0 | 0 | 0 | 0 | **0** | 1.14% |
| 1 / 0.10 | 0 | 0 | 0 | 0 | **0** | 1.14% |
| 2 / 0.05 (recommended) | 0 | 1 | 0 | alexa 1 | **1** | 1.07% |
| 4 / 0.02 | 0 | 0 | 0 | alexa 1 | **0** | 1.07% |
| 8 / 0.01 | **12** | 0 | 0 | alexa 6 | **12** | 1.11% |

| openWakeWord threshold | alexa | hey_jarvis | hey_mycroft |
|---|---|---|---|
| 0.3 | 2 | 2 | 0 |
| 0.5 (default) | 2 | 0 | 0 |
| 0.7 | 2 | 0 | 0 |
| 0.9 | 0 | 0 | 0 |
| 0.3 / 0.5 with OWW's built-in VAD gate (0.5) | 2 / 2 | 1 / 0 | 0 / 0 |

The gated break-glass fired **0 times** on the hour at every setting. Even the ungated stage-2 spotter heard "stop/cancel everything" 0 times in the hour, so the hour does not exercise the gate; section 3.4 does. The same "alexa" event (t = 1292 s) appears at boost 2 and 4, so it is probably a real near-match in the audio rather than random noise. The hour contains no deliberately adversarial sound-alikes; real TV that says "Alfred" or "Jarvis" would add some.

### 3.2 Detection rate and latency, synthetic voices in background audio

Detection rate in percent, 24 clips per cell. Latency is ms after the end of the word, mean / p95, clean audio.

**sherpa-onnx, boost 2 / threshold 0.05 (recommended default)**

| phrase | clean | 20 dB | 10 dB | 5 dB | 0 dB | latency clean |
|---|---|---|---|---|---|---|
| alfred | 92 | 96 | 79 | 71 | 46 | 407 / 617 |
| alfred (in "alfred, stop everything") | 96 | 92 | 75 | 54 | 42 | 375 / 554 |
| alfred (in "alfred, cancel everything") | 96 | 92 | 79 | 71 | 46 | 328 / 630 |
| jarvis | 67 | 67 | 54 | 33 | 25 | 268 / 590 |
| hey jarvis | 96 | 92 | 79 | 67 | 50 | 272 / 450 |
| hey footman | 88 | 88 | 58 | 29 | 12 | 339 / 484 |
| alexa | 38 | 62 | 50 | 33 | 25 | 430 / 578 |
| hey mycroft | 0 | 0 | 0 | 0 | 0 | - |

At boost 4 / 0.02, "alfred" reaches 96 / 100 / 83 / 75 / 54 and "hey footman" 96 / 92 / 62 / 33 / 17, with the same 0 false wakes per hour. At boost 1 it is nearly the same as boost 2 (alfred 92 / 92 / 75 / 58 / 42). Wrong-keyword fires (a different wake word firing on a clip) are 0-4% up to boost 4, and up to 29% at boost 8.

**openWakeWord, threshold 0.5 (default)**

| phrase | clean | 20 dB | 10 dB | 5 dB | 0 dB | latency clean |
|---|---|---|---|---|---|---|
| hey jarvis | 100 | 100 | 96 | 92 | 67 | -127 / 21 |
| alexa | 96 | 96 | 71 | 67 | 46 | -82 / 20 |
| hey mycroft | 100 | 96 | 75 | 62 | 29 | -170 / 17 |
| "jarvis" alone | 50 | 42 | 33 | 29 | 21 | 49 / 238 |
| alfred, hey footman, stop / cancel phrases | 0 | 0 | 0 | 0 | 0 | no ready-made model |

OWW's negative latencies mean it fires before the -40 dB trailing end of the word, so it is effectively 0.1-0.2 s quicker than sherpa KWS (the transducer needs trailing context). **Caution:** OpenWakeWord's models were trained on synthetic Piper-style speech and these clips are Piper speech, so its rates here are probably optimistic. The sherpa model was trained on real speech (GigaSpeech), so synthetic clips are out of domain for it and its rates may be pessimistic. The real-voice live test (section 7) is what separates the two.

Takeaways: sherpa's accuracy depends on how the word tokenises and how well the TTS pronounces it. "Alfred" and the two-word phrases are fine. Single short words ("jarvis", "alexa") and unusual words ("mycroft") are poor, and the 0% for "hey mycroft" and 38% for "alexa" may be partly a TTS artefact. A "hey" prefix helps short names. The persona name "Alfred" is a good fit.

### 3.3 Detection vs sensitivity (sherpa; clean / 10 dB / 0 dB)

| boost / threshold | alfred hit % | alfred false per hour | hey footman hit % | note |
|---|---|---|---|---|
| 1 / 0.25 | 92 / 75 / 42 | 0 | 83 / 54 / 12 | library default |
| 1 / 0.10 | 92 / 75 / 42 | 0 | 83 / 54 / 12 | threshold alone changes almost nothing |
| 2 / 0.05 | 92 / 79 / 46 | 0 | 88 / 58 / 12 | **recommended** |
| 4 / 0.02 | 96 / 83 / 54 | 0 | 96 / 62 / 17 | upper end of the safe range |
| 8 / 0.01 | 79 / 79 / 54 | **12** | 100 / 58 / 21 | too loose: false wakes, wrong-keyword fires, lower clean recall |

### 3.4 Two-stage break-glass (sherpa; wake boost 2 / threshold 0.05, stage 2 boost 1 / threshold 0.25)

Design tested (ADR-0017): a wake spotter always runs. When "alfred" fires it opens a **4 s window** and starts a second spotter for `STOP EVERYTHING` / `CANCEL EVERYTHING` over the audio from the last 2 s onward (the wake word is reported about 0.4 s late and the phrase can start right after the name). The break-glass fires only inside that window, so stage 2 costs nothing while idle.

| clip | metric | clean | 20 dB | 10 dB | 5 dB | 0 dB |
|---|---|---|---|---|---|---|
| "Alfred, stop everything" | break-glass fired (right phrase) | 96% | 92% | 75% | 46% | 21% |
| | stage 2 alone heard it (ungated) | 100% | 100% | 100% | 83% | 50% |
| "Alfred, cancel everything" | break-glass fired (right phrase) | 96% | 92% | 79% | 62% | 25% |
| | stage 2 alone heard it | 100% | 100% | 92% | 88% | 46% |
| "Stop everything" (no wake word) | **break-glass fired** | **0%** | **0%** | **0%** | **0%** | **0%** |
| | stage 2 alone heard it | 88% | 92% | 75% | 46% | 25% |
| "Cancel everything" (no wake word) | **break-glass fired** | **0%** | **0%** | **0%** | **0%** | **0%** |
| | stage 2 alone heard it | 92% | 100% | 88% | 62% | 54% |

- **Reliability:** stage 2 is reliable (100% clean, 88-100% at 20 dB). The gated fire rate is capped by the wake-word rate, so the break-glass is only as reliable as "Alfred" is in that noise. That supports keeping the hotkey and tray icon as the guaranteed stop path (ADR-0017).
- **Never fires without the wake word:** 0 fires in 240 no-wake clips (2 phrases x 24 x 5 SNRs) at boost 1, 2 and 4, and no wrong-phrase fires either. At boost 8 it did fire falsely (4-17% of "cancel everything" clips) because the wake spotter spuriously heard "alfred"; that is the practical ceiling on sensitivity.
- **Latency:** the gated fire comes 0.05-0.15 s (mean; p95 about 0.3 s) after the end of the speech.
- **Stage-2 sensitivity** (boost/threshold 1/0.25, 2/0.1, 4/0.05, 8/0.02, wake fixed at 2/0.05): boost 1 to 4 all give 92-96% gated fire in clean audio; boost 8 is worse (71% clean) because over-boosting makes the spotter miss. Keep stage 2 at boost 1-2.

### 3.5 VAD (Silero v5 via sherpa-onnx; threshold 0.5, 512-sample windows, min silence 0.25 s)

| measurement | result |
|---|---|
| CPU | 8.6 s for 61 min = **0.23% of one core** (428x real time) |
| Hour file | 736 segments; 68.3% of the time flagged as speech (the hour is about 74% speech by construction, with music beds) |
| Keyword clips: utterance at least 50% covered | 100% clean, 100% at 20 dB, 99% at 10 dB, 100% at 5 dB, 99% at 0 dB |
| Onset delay (VAD flag minus true speech start), clean | median **215 ms**, p95 265 ms (includes the 0.1 s minimum-speech setting) |
| VAD already on at the keyword start (background speech/music) | 0% clean, **66-70%** with background at 20 to 0 dB |

Use it as a cheap "someone is speaking" gate and as the utterance endpointer for dictation. It does **not** help against TV or other speakers, since it cannot tell them from the owner (design section 29: speaker recognition is the answer, later). openWakeWord's own VAD gate did not remove its false wakes on this hour and added about 27% CPU (4.0% vs 3.2%).

### 3.6 CPU cost while idle-listening

Process CPU time (user+sys, `getrusage` / `process_time`) while streaming the 61-minute hour at 100 ms chunks on the 12-core desktop CPU, one thread each. The machine had a load average of about 6 from other jobs, so the real-time factors are conservative.

| what | CPU, % of one core | x real time |
|---|---|---|
| sherpa-onnx KWS, wake only (3 product names + 3 test names) | **1.1%** | 88-94x |
| sherpa-onnx wake + stage-2 spotter always on (not the design; gated, idle cost stays 1.1%) | 2.6% | 38x |
| openWakeWord, 3 models, Python | 3.2% | 32x |
| openWakeWord + its Silero VAD gate | 4.0% | 25x |
| Silero VAD alone | 0.23% | 428x |

All of these are small for one core. Model weights are small too (sherpa KWS about 5 MB int8; OWW about 5 MB in total for all bundled models).

## 4. Caveats

- Synthetic voices, no real room, no real microphone, no distance. SNR here means speech level vs background excerpt level, not a room impulse response, so nothing yet says how 1, 2 and 3 m behave.
- 61 minutes: a zero count bounds the true rate to about 3 per hour (95%). Showing "< 1 per hour" needs about 3 clean hours or a stronger argument.
- The sherpa model never saw TTS speech in training and OWW was trained on it; compare the engines on real voices before trusting the ranking.
- Results are specific to the words tested; names tokenise differently.
- FMA CC tracks include NonCommercial / NoDerivatives licences. They are used only locally and are not committed or redistributed.
- Silero VAD printed one "circular buffer overflow" warning on the hour file (continuous speech beyond the 30 s buffer). Handled by the library; harmless here.
- sherpa-onnx Python wheel 2.0.3 had no espeak frontend for the Piper voices, so the TTS side is pinned to 1.13.8, the same version as the Go module.

## 5. How to reproduce

```
cd spikes/wake-word
./setup.sh        # venv, models, ~1 h background corpus, synthetic clips, Go build (CPU only)
./run_all.sh      # every table above into data/results/
```

Harness commands (after `go build -o data/wakeword .`): `kws -wav f.wav` (detections with timestamps), `vad -wav f.wav`, `hour`, `pos`, `vadtest`, `oww-hour`, `oww-pos`, `listen`. Python helpers are in `py/`.

## 6. Getting to under 1 false wake per hour with margin

1. **Collect more negative hours.** Run `hour` over 5-10 h of real TV, radio and podcasts at the owner's usual volume (it takes any 16 kHz wav). Target: zero events in at least 3 h at the default.
2. **Keep the default in the 1-4 boost range** and expose boost as the user-facing sensitivity (strict / normal / loose). Do not tune the threshold.
3. **Cheap second check before acting:** after a wake, require speech (VAD) and a recognised command within about 3 s, otherwise discard silently; an isolated spurious "alfred" then costs only a log line. The break-glass is already gated this way.
4. **Owner voice verification** (design section 29) so a TV saying "Alfred" does not open a command window.
5. **Custom OWW model** for "alfred" / "hey footman" from the later training kit if sherpa KWS proves too weak for a chosen name in the live test.

## 7. Owner live test (under 5 minutes; you open the microphone)

The automated spike never opened the microphone. This opens the default input and prints detections. Audio goes straight from `parec` into the detector; nothing is saved.

**One-time prep (about 2 min, CPU only):**

```
cd spikes/wake-word && LIVE_ONLY=1 ./setup.sh
```

**Run (about 4 min):**

```
./live_test.sh sherpa 0.05 2        # sherpa-onnx KWS, boost 2 / threshold 0.05 (the recommended default)
```

It prints lines such as `3.2s  wake ALFRED` and `5.1s  bg   STOP_EVERYTHING`. The same thing without the script:

```
parec --format=float32le --rate=16000 --channels=1 | data/wakeword listen -thr 0.05 -boost 2
```

Mark 1 m, 2 m and 3 m from the microphone. Quiet room, normal speaking voice, facing the mic. At **each distance**, about 3 s apart:

1. Say **"Alfred"** 10 times.
2. Say **"Alfred, stop everything"** 5 times.

Then at **1 m only**:

3. Say **"Hey Footman"** 5 times and **"Jarvis"** 5 times.
4. Say **"stop everything"** (no "Alfred") 5 times. Any `bg` line here is a failure.
5. Leave it running for 1 minute while you talk normally or play the TV, and count any `wake` lines (false wakes).

Record counts (Ctrl-C to stop):

| distance | "Alfred" out of 10 | "Alfred, stop everything": `wake` out of 5 | then `bg STOP_EVERYTHING` out of 5 | notes |
|---|---|---|---|---|
| 1 m | | | | |
| 2 m | | | | |
| 3 m | | | | |

| 1 m only | count | notes |
|---|---|---|
| "Hey Footman" out of 5 | | |
| "Jarvis" out of 5 | | |
| `bg` lines for bare "stop everything" (want 0) | | |
| false wakes in 1 min of talk or TV | | |

If "Alfred" misses at 2-3 m, repeat steps 1-2 at that distance with `./live_test.sh sherpa 0.02 4` and note it. Optionally repeat the wake part with openWakeWord (`./live_test.sh oww 0.5`) saying **"Hey Jarvis"** (it has no Alfred model). Paste the tables into the issue.
