# testdata

Audio clips, recorded LLM responses and golden files used by tests. Keep fixtures small.

- `speech/`: a 5-second and a 10-second LibriSpeech test-clean clip for the native speech engine tests and the end-to-end push-to-talk test (CC-BY-4.0; see `speech/LICENSE.txt`).
- `wake/`: short synthetic clips for the wake-word tests (`gen.py` makes them): "Alfred", "Hey Footman", "Alfred, stop everything" and "cancel everything", and negatives (the stop phrases without the wake word, other names, ordinary sentences). Voices are LibriTTS-R speakers (CC-BY-4.0; see `wake/LICENSE.txt`).
