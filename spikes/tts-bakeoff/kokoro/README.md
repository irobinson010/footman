# Kokoro style-vector fit for the Ashford voice (S0-10, Kokoro half)

Kokoro-82M stays frozen. What is trained is one 256-d offset added to a stock voice pack
(`bm_daniel`), by gradient descent through the model: `[:128]` steers the decoder (timbre),
`[128:]` the prosody predictor (rhythm, pitch, energy). The result is a 510x256 table that is
appended to `voices.bin` as one more speaker (`bm_ashford`), so Footman loads it like any Kokoro
voice (ADR-0037), no code change. About 3 minutes per epoch of 1,780 lines, about 5 GB VRAM.

Loss per kept line: multi-resolution log-mel L1 against the real clip with per-phoneme durations
taken from a DTW alignment (re-done every step with the current style), a log-L1 pulling the
predictor's own durations to those, and (`--spk-weight`) 1 - cosine of the ECAPA-TDNN embedding
of the output against the centroid of the dataset. Phonemes come from Footman's own front end
(`TestDumpPiperPhonemes` with `FOOTMAN_PHONEMES_FAMILY=kokoro`), so training sees what runtime feeds.

    setup.sh                                   # venv, weights, phonemes (outside git)
    python rank_voices.py                      # which stock voice to start from (bm_daniel)
    python train_style.py RUN --init bm_daniel --epochs 3 --lr 0.005 --spk-weight 1 --save-steps 75
    ./finish.sh RUN/ashford_table.pt TAG       # export, render through Footman, WER, UTMOS, speaker sim
    python add_to_ab.py AB_DIR kokoro "name" renders/TAG    # add to the blind page

Files: `common.py` (model, differentiable forward), `fit_lib.py` (data, losses, DTW, ECAPA loss),
`export_voice.py` (voices.bin + model.onnx speaker metadata, stock folder untouched),
`spk_sim.py` (ECAPA and WavLM-SV cosine), `add_to_ab.py`, `delta_to_table.py`.

Notes
- The dataset WAVs are 48 kHz (not 24 kHz); they are resampled to 24 kHz on load.
- Fitting ECAPA directly overfits quickly: with weight 3 and lr 0.01, UTMOS fell from 4.06 (epoch 1)
  to 3.31 (epoch 3) while the independent WavLM scorer also dropped. Weight 1, lr 0.005, 3 epochs is stable.
- Licence: Kokoro-82M is Apache-2.0. The dataset voice is an ElevenLabs design, personal use only
  (ADR-0019/0020). Models, tables and audio are not in git.
