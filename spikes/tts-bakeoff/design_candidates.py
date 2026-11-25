#!/usr/bin/env python3
"""Generate Ashford voice-design previews with the ElevenLabs Voice Design API (S0-09).

Reads ELEVEN_LABS_API from the environment, or from the .env file named by $ASHFORD_ENV_FILE.
The key is never printed or written. Only POST /v1/text-to-voice/design is called, never
/v1/text-to-voice, so nothing is saved to the voice library.
Writes candidates/{A..F}.mp3 and candidates/key.json (gitignored).
"""
import base64
import json
import os
import sys
import urllib.error
import urllib.request
from pathlib import Path

OUT = Path(__file__).parent / "candidates"
LINE = ("Good evening. I've taken the liberty of drafting your reply. "
        "Shall I send it, or would you prefer to make it more polite first?")
PROMPTS = [
    "An original character: a dignified, elderly British butler. Deep, warm, measured baritone, "
    "refined received-pronunciation accent, unhurried pace, gentle dry wit, a hint of a smile in "
    "the voice. Calm, courteous and precise.",
    "An older English gentleman's gentleman in his late sixties: mellow, slightly gravelly voice, "
    "crisp consonants, formal and understated, wry and affectionate, speaking softly and clearly "
    "like a trusted household steward. Not theatrical.",
]


def key():
    k = os.environ.get("ELEVEN_LABS_API")
    if not k and os.environ.get("ASHFORD_ENV_FILE"):
        for ln in Path(os.environ["ASHFORD_ENV_FILE"]).read_text().splitlines():
            if ln.startswith("ELEVEN_LABS_API="):
                k = ln.split("=", 1)[1].strip().strip("'\"")
    if not k:
        sys.exit("ELEVEN_LABS_API not found")
    return k


def main():
    OUT.mkdir(exist_ok=True)
    mapping, letters, chars = {}, iter("ABCDEF"), 0
    for p in PROMPTS:
        body = {"voice_description": p, "text": LINE, "auto_generate_text": False,
                "model_id": "eleven_multilingual_ttv_v2", "guidance_scale": 5}
        req = urllib.request.Request(
            "https://api.elevenlabs.io/v1/text-to-voice/design",
            data=json.dumps(body).encode(),
            headers={"xi-api-key": key(), "Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=180) as r:
                reply = json.loads(r.read())
        except urllib.error.HTTPError as e:
            sys.exit(f"HTTP {e.code}: {e.read().decode()[:200]}")
        chars += len(LINE)
        for pv in reply["previews"]:
            L = next(letters)
            (OUT / f"{L}.mp3").write_bytes(base64.b64decode(pv["audio_base_64"]))
            mapping[L] = {"prompt": p, "generated_voice_id": pv.get("generated_voice_id"),
                          "model_id": body["model_id"], "text": LINE,
                          "provenance": "ElevenLabs Voice Design preview, not saved to library"}
    (OUT / "key.json").write_text(json.dumps(mapping, indent=2))
    print(f"wrote {len(mapping)} previews; text characters sent: {chars}")


if __name__ == "__main__":
    main()
