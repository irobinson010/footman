# 0019. Voice source licensing

- Status: Accepted (amended by 0020)
- Date: 2026-11-24

## Context
ADR-0013 plans to generate 1–3 hours of speech with a large voice source, fine-tune a small model (Kokoro/StyleTTS2 or Piper/VITS) on it, and possibly **ship that trained model** in this Apache-2.0 repo as an original persona (ADR-0012). Each source has its own terms on training on its outputs and on redistributing derived models. Issue #11 asks which are allowed for personal use and which for shipping.

All terms below were read from primary sources on **2026-11-24** (URLs in the table and the source notes). **This is not legal advice.** It is a good-faith reading by the project, and a lawyer should review it before anyone relies on it commercially. Where a source is unclear, this ADR says so and takes the conservative reading.

## Decision

**Shipped voices (public repo, Apache-2.0)** may only use this pipeline:

1. **Voice design and reference clip:** VoxCPM2's own voice design or cloning (Apache-2.0), or a recording from a person who has agreed in writing to the use. **Not ElevenLabs.** The reference must not imitate an identifiable real person (ADR-0012).
2. **Dataset generation:** **VoxCPM2** only (code and weights Apache-2.0). Text comes from the CC0 or public-domain sources listed under "Training text".
3. **Small model:** fine-tune **Kokoro-82M** (Apache-2.0 weights; code via StyleTTS2, MIT) or **Piper/VITS from a permissive base checkpoint**: a CC0 or public-domain chain only, such as `nl_BE-rdh-medium` (CC0) or `en_US-ljspeech` (public domain). **Never** from the Lessac or Ryan chains, and avoid CC BY-SA and CC BY bases for shipped voices unless attribution is carried (see conditions).
4. **Ship with the voice:** a `MODEL_CARD` stating the full training chain, the licenses, a note that the voice is synthetic, and the notices each license requires.

**Personal use only (never committed, never published, never put in a release):**
- **ElevenLabs** (Voice Design or any Output), at any plan. Its use policy bars using Output to train any AI model unless the terms expressly allow it, and they do not.
- **IndexTTS-2.5**. Its license lets you use it or its outputs to improve only non-commercial models. An Apache-2.0 repo cannot honour that.
- **Fish Audio S2 Pro**. The weights are research and non-commercial only, and whether training a small voice model on its output is allowed is unclear.

A personal-use voice can still be used on the owner's machine. Mixing any personal-use output into a shipped voice's reference or dataset **taints** it; a shipped voice must have a clean chain.

**Conservative choices where terms are unclear:**
- Treat a model trained on a source's output as a *derivative of that source* whenever the source's license says so (IndexTTS, Fish), even if copyright law might not.
- Do not use ElevenLabs output even as a reference clip for a shipped voice. The terms do not expressly cover it, and the use policy's wording ("including Output in a dataset") is broad.
- Do not rely on an unread or unavailable license. Gutenberg's policy page was down on the day of review (see Training text).

## Source terms

| Source | Personal use | Training a model on its outputs | Publicly distributing a derived voice | Conditions / links |
|---|---|---|---|---|
| **ElevenLabs** Voice Design and TTS output | Yes. You keep rights in Output (ToS 4(c)(ii)) and may use it outside the service (4(a)). **Free plan: non-commercial only; paid plans may use commercially** (ToS 1(c)). | **No, on any plan.** Use policy s.12 bars "Using our Services or Output to develop, train, fine-tune, or improve any AI model or service", "except as expressly permitted under the applicable terms". The ToS grant no such permission. | **No** (it would require training on the output). | The ToS (last updated 2026-10-09) never mention "Voice Design", so its outputs are treated as ordinary Output. Plan only changes commercial use, not the training ban. Also no cloning real people without consent, and no removing watermarks. [ToS](https://elevenlabs.io/terms-of-use), [Use policy](https://elevenlabs.io/use-policy) (read 2026-11-24). |
| **IndexTTS-2.5** (bilibili/index-tts) | Yes, under the bilibili Model Use License. Free for most users; separate license needed above 100M MAU or RMB 1B revenue (2.2). | **Non-commercial models only.** 3.4(c): you "may not Use the bilibili indextts2 or any Derivative Work to improve any AI model, except for ... non-commercial AI models". The license also defines any model fine-tuned on "model outputs" as a Derivative Work (1.5). | **No, for this repo.** A Derivative Work may be distributed only with the license text, the 4.1(a) disclaimer and downstream terms binding recipients (3.4(a)-(b)), and 3.4(c) still limits it to non-commercial use. Apache-2.0 is not non-commercial. | One license covers code and weights; it is not an OSI license. Chinese law governs, Chinese text prevails (6, 9). The GitHub page says only "released under the bilibili Model Use License Agreement" and points commercial users to indexspeech@bilibili.com. [LICENSE](https://github.com/index-tts/index-tts/blob/main/LICENSE), [README](https://github.com/index-tts/index-tts) (read 2026-11-24). |
| **VoxCPM2** (openbmb) | Yes. | **Yes.** Apache-2.0 on weights and code; the model card says "free for commercial use" and puts no restriction on training with outputs. | **Yes** (Apache-2.0). | Model card: "Strictly forbidden to use for impersonation, fraud, or disinformation. AI-generated content should be clearly labeled." So label shipped voices as synthetic and never imitate a real person. The card does not describe the audio it was trained on; not independently verified. [Model card](https://huggingface.co/openbmb/VoxCPM2), [code LICENSE](https://github.com/OpenBMB/VoxCPM/blob/main/LICENSE) (read 2026-11-24). |
| **Fish Audio S2 Pro** (fishaudio/s2-pro) | Yes. The Fish Audio Research License (2026-03-07) allows personal, hobbyist and research use free of charge. | **Unclear.** IV(b) bars using the Materials or output "to create or improve any foundational generative AI model". A small single-voice model is arguably not "foundational", but the text is untested. Conservative reading: do not. | **No.** Commercial use needs a separate license (III), and the Materials define a model "based on or derived from ... the Model's output" as a Derivative Work. A public Apache-2.0 repo is not non-commercial. | The S2-Pro HF page and its LICENSE.md use the Research License. The `fish-speech` repo LICENSE is a **different, newer** "Fish Audio Community License v1.0" (2026-10-13) that covers Drama 3 and the codec, **not** S2-Pro weights. Under that v1.0 text, training any "speech, audio, or voice generation model" on output is barred outright. Expect the S2-Pro terms to tighten; re-check before relying on them. [HF LICENSE.md](https://huggingface.co/fishaudio/s2-pro/blob/main/LICENSE.md), [repo LICENSE](https://github.com/fishaudio/fish-speech/blob/main/LICENSE) (read 2026-11-24). |
| **Kokoro-82M** (hexgrad) | Yes. | Yes as a training target (it is the model being fine-tuned). | **Yes.** Apache-2.0 weights; the card says they can be "deployed anywhere". Fine-tuned derivatives may be redistributed under Apache-2.0 with its notice. | The card says it was trained only on "permissive/non-copyrighted audio" plus synthetic audio from closed TTS providers, and lists some CC BY 4.0 / 3.0 data (SIWIS, Koniwa), so keep credit in the `MODEL_CARD`. I found no training code published with the model card; fine-tune using StyleTTS2's code. [Model card](https://huggingface.co/hexgrad/Kokoro-82M) (read 2026-11-24). |
| **StyleTTS2** (yl4579) | Yes. | Yes as a training target. | **Yes, with a condition on the pretrained checkpoints.** Code is MIT. | Pretrained models (LJSpeech, LibriTTS): you agree to tell listeners the speech is synthetic, unless the voice's owner has permitted cloning. Our personas are synthetic and labeled, so this is met. Inference depends on a GPL phonemizer (espeak-ng), which matters for how Footman links it; a follow-up item. [LICENSE](https://github.com/yl4579/StyleTTS2/blob/main/LICENSE), [README](https://github.com/yl4579/StyleTTS2) (read 2026-11-24). |
| **Piper** engine and voices (OHF-Voice/piper1-gpl, rhasspy) | Yes. | Yes as a training target. | **Depends entirely on the base voice's dataset chain.** The Piper project grants no license over voices and takes no position on whether a model is a derivative of its corpus. | Engine is **GPL-3.0-or-later**; the old `rhasspy/piper-voices` repo is tagged MIT, but that is not a grant of rights in the datasets. **Do not** fine-tune from `en_US-lessac` (corpus limited to research) or the Ryan chain. Safe bases listed in Piper's COMMERCIAL_USE doc: `nl_BE-rdh-medium` (CC0), `en_US-ljspeech` (public domain), `en_US-john/kristin/norman` (public domain). CC BY bases such as `en_US-hifi_tts_92` carry attribution into every derivative; CC BY-SA is an open question. Retrained voices keep their old names, so read the `MODEL_CARD`'s Training line for the copy you actually have. [LICENSING](https://github.com/OHF-Voice/piper1-gpl/blob/main/docs/LICENSING.md), [COMMERCIAL_USE](https://github.com/OHF-Voice/piper1-gpl/blob/main/docs/COMMERCIAL_USE.md), [checkpoints](https://huggingface.co/datasets/rhasspy/piper-checkpoints) (read 2026-11-24). |

### Training text

The text script drives what the voice says, so it must not carry its own license problem. Recommended, in order:

1. **Text the maintainers write** (or an LLM writes for them) in the personas' own style. No third-party rights; best for persona lines and system phrases.
2. **LJSpeech-derived text**: public-domain books published 1884-1964; the dataset page says all text, audio and annotations are in the public domain. [keithito.com/LJ-Speech-Dataset](https://keithito.com/LJ-Speech-Dataset/) (read 2026-11-24).
3. **Common Voice Sentence Collector sentences**: the collector accepts only public-domain sentences ("The source does not matter, as long as the sentences are in the public domain"). [README](https://github.com/common-voice/sentence-collector) (read 2026-11-24). I did not confirm the CC0 label on the exported corpus; the page I tried returned no license text.
4. **Project Gutenberg and Wikisource** public-domain works. **Not verified:** Gutenberg's permission pages returned 503 on 2026-11-24 and I did not read Wikisource's terms. Gutenberg works are generally public domain in the US only, so check each work's author and publication date and strip the Gutenberg header and trademark text.

Avoid Wikipedia and anything under CC BY-SA or all-rights-reserved text.

## Consequences
- **Blocks the plan as written.** ElevenLabs Voice Design cannot be the source for a shipped voice, and neither can IndexTTS-2.5 or S2 Pro. The cheap overnight dataset path through Troupe must use **VoxCPM2** for anything shipped. The bake-off (design §31) can still compare all engines for personal listening.
- Design §22 item 6 and §31 step 1 need updating: replace "ElevenLabs Voice Design" as the shipped-voice source with VoxCPM2 voice design.
- Track each shipped voice's provenance in its `MODEL_CARD`: source engine and version, reference clip origin, text sources, base checkpoint chain, and all licenses.
- Open items for a human: (a) lawyer review of whether a voice trained on a model's output is a derivative of that model; (b) GPL espeak-ng and Piper engine linking against Footman's Apache-2.0 core; (c) whether purely AI-generated audio has any copyright, which affects whether the project can license the shipped voice at all; (d) re-read Gutenberg and Wikisource terms; (e) VoxCPM2's own training data is not documented on its card.
- Licenses change: S2-Pro already has two different texts in two places. Re-run this review before each public voice release.
