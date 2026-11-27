# S0-09 GPU half: render the Ashford dataset

Status: planned, nothing rendered. The first half (candidates, listening page, script) was done with no GPU, no Docker and no Troupe API calls, because Troupe was rendering overnight.

## Inputs

| Input | Where | Provenance (ADR-0020) |
|---|---|---|
| Voice candidates A-F | `../candidates/` (gitignored), key in `candidates/key.json` | ElevenLabs Voice Design previews (`eleven_multilingual_ttv_v2`, seed unset), not saved to the library. Personal-use source per ADR-0019. |
| Script | `script.tsv`, 2,015 lines (id, text, source) | 900 CMU ARCTIC prompts (Gutenberg public-domain text), 600 Harvard sentences, 515 self-written (CC0). Built by `build_script.py`, deterministic. |
| Reference clip | owner-chosen candidate, about 60 s | See "Reference clip" below. |

Script shape: 2,015 lines, 2-3 s each on average (mean 8.7 words, max 15; 20 short assistant phrases are under 4 words on purpose, per the issue's "Right away."). About 17.5k words, so roughly 1.9 hours of audio. About 123 questions, 59 exclamations, 267 lines with digits. Re-run `build_script.py --cache DIR` to regenerate it exactly.

## Reference clip (owner decides first)

1. Owner ranks A-F in `../listen.html` and names a winner.
2. A design call returns only about 8 s per preview, and the clone needs about a minute. Take the winner's `generated_voice_id` and either (a) save it to the ElevenLabs library, then run several short TTS calls with it (the only step that needs a saved voice; roughly 900 characters for a minute), or (b) clone from the 8 s preview alone. Try (b) first, since Troupe's "shift then clone" and prompt-transcript findings suggest short clean references work.
3. Convert to mono WAV (`ffmpeg -i X.mp3 -ac 1 -ar 24000 ref.wav`) and keep the transcript beside it. The same text goes in the model card.
4. Check it with Troupe's voice analyzer (age, accent, pitch) and make sure the accent comes out British and the age 60+.

## Engine choice

Troupe ADR 0002 (updated 2026-10-01): the engine is chosen per voice, with no single winner. Plan: render a 5-line smoke test with each of IndexTTS-2.5 and VoxCPM2, let the owner pick by ear, and render the full set with one.

Default order: VoxCPM2 first. Reasons: it is Apache-2.0 (clean chain for ADR-0019 if the voice later ships; this needs a clean reference, and an ElevenLabs reference taints it), it is Troupe's default, 20 inference steps matched or beat 10 on every voice, and it has a much longer max_chars (400) margin than needed. IndexTTS-2.5 is faster (RTF 0.43 vs 0.52) but its license limits training to non-commercial models. Both are acceptable under ADR-0020 for now.

## Rules (from Troupe's ADR 0008 and AGENTS.md)

- One GPU task at a time. Check `docker ps` for any `troupe-S*` or `tts-*` container and `nvidia-smi` first. Wait until Troupe's overnight queue is empty. Do not stop Troupe's containers.
- Do not go through Troupe's worker or API. Start a throwaway container on a free port, like `spikes/engine-audition` does (VoxCPM2 on 8092, IndexTTS on 8095), then stop and remove it.
- Never touch the always-on S2 server on 8090.

## Commands

All paths are relative to the Troupe checkout `T=~/PersonalProjects/reading_text_in_real_voice` and the Footman checkout `F=<this repo>`. Output goes under `$F/spikes/tts-bakeoff/dataset/out/` (add to `.gitignore`; do not commit audio).

```bash
T=~/PersonalProjects/reading_text_in_real_voice
F=$(git rev-parse --show-toplevel)
D=$F/spikes/tts-bakeoff/dataset
REF=$D/ref/ashford.wav      # about 60 s, mono, from the winning candidate

# 0. GPU must be free
docker ps; nvidia-smi --query-gpu=memory.used --format=csv

# 1a. VoxCPM2 (default)
docker rm -f footman-S0-09-voxcpm2
docker run -d --gpus all --name footman-S0-09-voxcpm2 \
  --user "$(id -u):$(id -g)" -e HOME=/models -e INFERENCE_TIMESTEPS=20 \
  -p 127.0.0.1:8092:8092 -v "$T/models/voxcpm2:/models:ro" troupe-tts-voxcpm2
until curl -s http://127.0.0.1:8092/health | grep -q '"ready": *true'; do sleep 5; done

# 2. Smoke test, then listen to 5 lines
python3 $D/render_dataset.py --url http://127.0.0.1:8092 --ref $REF --out $D/out/voxcpm2 --limit 5

# 3. Full render (resumable; Ctrl-C and re-run is safe)
python3 $D/render_dataset.py --url http://127.0.0.1:8092 --ref $REF --out $D/out/voxcpm2

# 4. Free the GPU straight away
docker stop footman-S0-09-voxcpm2 && docker rm -f footman-S0-09-voxcpm2

# 5. Verify on the CPU (no GPU needed; run it in Troupe's asr-verify env for faster-whisper, or a venv with
#    faster-whisper, speechbrain, torch CPU, numpy, soundfile)
cd $T/spikes/voice-drift && uv run python $D/verify_dataset.py \
  --out $D/out/voxcpm2 --ref $REF --troupe $T
```

IndexTTS-2.5 alternative: same steps with `troupe-tts-indextts`, port 8095, `-v "$T/models/indextts:/models:ro"`, no `-e HOME`, `--out $D/out/indextts`.

`render_dataset.py` and `verify_dataset.py` are untested against live services (no GPU tonight). The first hour of GPU time should start with `--limit 5`, and if the `/generate` shape differs from Troupe's adapters (`src/troupe/engines/voxcpm2.py`, `indextts.py`), fix the script before the full run. VoxCPM2's transcript-prompt mode (`VOXCPM2_CLONE_MODE=transcript`) is off here; the plain-reference mode is the simplest and gives one voice for every line.

## Checks (reused from Troupe, S0-07 and S5-21 calibration)

| Check | Rule | Source |
|---|---|---|
| ASR WER | `faster-whisper base.en`, CPU int8; drop when WER > 0.08 and at least 2 word errors | `spikes/asr-verify/wer.py`, `QAConfig.max_wer`, `min_word_errors` |
| Speed band | chars per second between 6 and 25 | `QAConfig.min/max_chars_per_second` |
| Dead tail | RMS of last second below -50 dB | `QAConfig.min_trailing_rms_db` |
| Voice drift | ECAPA cosine to the reference below 0.33, or more than 0.15 below this run's median, for lines of at least 3 s | `spikes/voice-drift/score.py`, `QAConfig.min_speaker_similarity`, `speaker_margin` |

Notes: the digit lines ("6:30", "1995", phone numbers) test number reading, and the WER normalizer expands digits, so they are scored fairly, but expect a few to be dropped for number readings that differ legitimately. Check the drop list by hand for those. Proper names (the template lines) will cost a little WER too. Re-render dropped lines once with a new seed (`--only ids.txt --seed 2`), then drop what still fails (Troupe's `max_attempts` is 3).

## Time and size

- Audio: about 1.9 h, 2,015 lines.
- Render: RTF 0.43 (IndexTTS) to 0.52 (VoxCPM2) means about 50-60 minutes, plus 1-3 minutes of container start, plus a second pass for dropped lines (about 5%): about 1.2 hours in total.
- Verify: `base.en` on CPU runs far faster than real time, and the ECAPA embedding is cheap, so about 15-25 minutes on the 12-core desktop CPU.
- VRAM: VoxCPM2 8.8 GB estimate (14.6 GB peak measured in the bake-off), IndexTTS 12.7 GB peak. Neither fits beside Troupe's LLM on 16 GB, so schedule it when Troupe's queue is empty.
- Disk: 24 kHz mono 16-bit WAV is about 5.5 GB for 1.9 h; 48 kHz from VoxCPM2 about 11 GB. Keep it out of git.

## Done when

`out/<engine>/manifest.jsonl` and `stats.json` exist, and `REPORT.md` records: hours and lines kept and dropped (by reason), median speaker similarity, RTF, VRAM, the chosen engine and why, and the provenance block (candidate letter and prompt, ElevenLabs voice id, reference clip and transcript, script sources, engine and version, date). The report should also list any lines dropped for numbers or names, to decide whether the script needs a pronunciation pass before training Kokoro or Piper (part 2).

## Open decisions for the owner

1. Which candidate (A-F), or "none, regenerate with other prompts".
2. Reference: 8 s preview only, or save the voice to the ElevenLabs library to make about 60 s.
3. Engine: VoxCPM2 or IndexTTS-2.5 (or both, compared by ear).
4. Whether the 20 under-4-word assistant phrases and the 267 digit lines stay in.
