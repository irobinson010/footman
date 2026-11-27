# S0-09 report: Ashford dataset (VoxCPM2)

Date: 2026-11-25. Status: done; the dataset is ready for part 2 (#10).

## Result

| | |
|---|---|
| Lines rendered | 2,015 (1.72 h) |
| Kept | **1,908 lines, 1.60 h** |
| Dropped | 107 (106 WER, 1 speaking rate) |
| Median speaker similarity to the reference (ECAPA) | 0.853; no line dropped for drift |
| Engine | VoxCPM2, 20 inference steps, plain-reference mode, seed 1 |
| RTF on the 16 GB NVIDIA GPU | 0.84 (87 min wall for 1.72 h); the plan's 0.52 estimate was low |
| VRAM | about 9.3 GB |

## Steps and decisions

1. **Owner ranking** of the blind candidates: 1=E, 2=B, 3=A, 4=C, 5=F, 6=D. E won (the "older English gentleman's gentleman" prompt).
2. **Reference:** E was saved to the owner's ElevenLabs library as "Footman Ashford" (after the Troupe session freed library slots). A 51 s reference was rendered with `eleven_multilingual_v2` (stability 0.6, similarity 0.8) from `reference-transcript.txt`, then converted to 24 kHz mono WAV. Audio and voice id stay out of git (`ref/` is ignored).
3. **Smoke test:** 11 lines (5 short phrases, 6 longer sentences, including a time). The owner judged it "fine, not the best voice but a good stock one", so IndexTTS-2.5 was not tried.
4. **Full render** with `render_dataset.py`; no errors and no retries.
5. **Verification** with `verify_dataset.py` (Troupe's thresholds): 198 flagged at first (115 WER, 94 silent tail, 1 duration).
6. **Second pass** (`postprocess.py`):
   - **Silent tails:** all 94 were good speech followed by more than a second of dead air. They were trimmed (to 250 ms after the last frame above -40 dBFS) rather than dropped.
   - **Dates:** 9 WER drops were only the ASR writing "the twentieth of November" as "20 November". They were rescued by a date-aware normalizer.

## What the remaining 107 drops are

- **Rare names** dominate: "Yusuf" heard as "use of", "Oyelaran", "Tomasz Haddad", "Mbeki". Most of that audio is probably correct; base.en cannot spell those names. It is safer to leave them out of a small-model dataset anyway.
- **Numbers:** 29 contain digits. These are phone numbers, prices ("30 pounds and 54 pence" heard as "£30.54") and times ("12:05" heard as "12 a.m. 5"). Some are real misreads; for example ash-0175's phone number came out as "3-3-4-4-4-4-4-9-9-5".
- **By source:** 46 self-written template lines, 41 CMU ARCTIC, 20 Harvard.

The redo list is `out/voxcpm2/redo.txt`. A seed-2 re-render was skipped: at 1.6 h kept, it would add at most about 5% for little gain.

## For part 2 (#10)

- Train on `manifest.jsonl` rows with `"verdict": "keep"`.
- Number reading is the weak spot. Before training, consider a text-normalization pass that spells numbers out (as the TTS front end will at run time), so the model is never asked to learn digit reading.

## Fixes to the scripts made during the run

- `verify_dataset.py` now decodes audio with the ffmpeg CLI. The PyAV wheel that faster-whisper installs refuses the `metadata_errors` argument.
- Run verification with `uv run --with faster-whisper --with num2words` from Troupe's `spikes/voice-drift` (Troupe's `wer.py` needs num2words).

## Provenance (ADR-0020)

- Voice: ElevenLabs Voice Design preview E (`eleven_multilingual_ttv_v2`), prompt in `../candidates/key.json` (gitignored), saved to the owner's library.
- Reference: ElevenLabs TTS of `reference-transcript.txt` (self-written, CC0).
- Script: `script.tsv` (CMU ARCTIC prompts from Gutenberg public-domain text, Harvard sentences, self-written CC0).
- Engine: Troupe's `troupe-tts-voxcpm2` image (VoxCPM2, Apache-2.0).
- Personal use only for now; ADR-0019's clean-chain requirement applies before any voice made from this dataset ships.
