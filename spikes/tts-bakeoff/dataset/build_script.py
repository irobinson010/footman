#!/usr/bin/env python3
"""Build dataset/script.tsv: ~2,000 English sentences for the Ashford TTS dataset (S0-09).

Sources (id prefix in the first column):
  own-*      written for this project (CC0): assistant phrases, numbers, dates, names, questions,
             exclamations, built from fixed lists plus a seeded template generator.
  harvard-*  the Harvard sentences (IEEE 1969, the standard phonetically balanced speech-test list;
             https://www.cs.columbia.edu/~hgs/audio/harvard.html).
  arctic-*   CMU ARCTIC prompts (cmuarctic.data, from out-of-copyright Project Gutenberg books;
             the ARCTIC databases are released without restriction; http://festvox.org/cmu_arctic/).
Selection from harvard/arctic is greedy on letter-trigram coverage, so the kept lines are as
phonetically varied as possible. Output is deterministic (seed 9).

    python3 build_script.py [--cache DIR]      # downloads the two source files if missing
"""
import argparse
import html
import random
import re
import urllib.request
from pathlib import Path

HARVARD_URL = "https://www.cs.columbia.edu/~hgs/audio/harvard.html"
ARCTIC_URL = "http://festvox.org/cmu_arctic/cmuarctic.data"
N_OWN, N_HARVARD, N_ARCTIC = 520, 600, 900
MIN_W, MAX_W = 4, 25

ASSISTANT = """Right away.
Of course.
Certainly, sir.
Certainly, madam.
Very good.
Shall I proceed?
Shall I send it?
Would you like me to read that aloud?
I'm afraid that didn't work.
One moment, please.
I've done that for you.
It's all taken care of.
Might I suggest a short break?
I beg your pardon?
Could you say that once more?
I didn't quite catch that.
Whenever you're ready.
I'll keep an eye on it.
Consider it done.
Would you like to hear the rest?
I've set a reminder for you.
Your message has been sent.
I have cancelled the timer.
The job is still running.
It has finished, and there were no errors.
I'm sorry, I can't do that without your permission.
Do you wish me to go ahead?
That would be unwise, if you don't mind my saying so.
I'm listening.
Good morning. Shall I read you the headlines?
Good evening. You have three unread messages.
Your meeting begins in ten minutes.
You asked me to remind you about the umbrella.
The kettle, I trust, is already on.
I took the liberty of closing the other windows.
If I may, the second draft reads rather better.
Allow me to fetch that for you.
Pardon the interruption, but it's nearly midnight.
Splendid. That's the last of them.
I shall be here if you need me.
Shall I draft a reply, or would you prefer to dictate one?
I'm afraid the connection has dropped again.
Nothing to report, I'm happy to say.
That is rather a lot of tabs, if I may say so.
Do take your time.
Shall I read it back to you?
Would you like it a little more polite?
I have saved your notes.
The file has been moved to your documents folder.
Is there anything else, sir?
Is there anything else, madam?
Heavens, what a surprise!
Well, that went rather well.
I should hardly call that a disaster.
Not at all.
Quite so.
Indeed it is.
Naturally.
Oh, dear.
Ah, there it is.
Splendid!
Wonderful news!
Stop!
Careful!
Good heavens, is that the time?
How very kind of you!
What a lovely surprise!
That's absolutely marvellous!""".split("\n")

FIRST = ["James", "Eleanor", "Hugh", "Margaret", "Oliver", "Beatrice", "Rupert", "Priya", "Tomasz", "Amara",
         "Siobhan", "Wei", "Yusuf", "Ingrid", "Mateo", "Nadia", "Gareth", "Philippa", "Dmitri", "Lakshmi",
         "Cormac", "Imogen", "Takeshi", "Fatima", "Barnaby", "Rosalind", "Kwame", "Astrid", "Lucian", "Marguerite"]
LAST = ["Ashworth", "Okafor", "Lindqvist", "Fitzgerald", "Nakamura", "Petrov", "Haddad", "Whitcombe",
        "Delacroix", "Abernathy", "Kowalski", "Ramirez", "Thackeray", "Mbeki", "Sorensen", "Pemberton",
        "Chaudhry", "Lockhart", "Vasquez", "Ainsworth", "Grimaldi", "Oyelaran", "Featherstone", "Zhang"]
CITY = ["Edinburgh", "Nairobi", "Reykjavik", "Lisbon", "Bangalore", "Toronto", "Wellington", "Cardiff",
        "Prague", "Marrakesh", "Chicago", "Valparaiso", "Gothenburg", "Dublin", "Kyoto", "Cincinnati",
        "Worcester", "Leicester", "Tucson", "Albuquerque", "Seoul", "Zurich", "Perth", "Mumbai"]
MONTH = ["January", "February", "March", "April", "May", "June", "July", "August", "September",
         "October", "November", "December"]
WEEKDAY = ["Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"]
THING = ["the invoice", "the report", "the garden party", "the dentist", "the delivery", "the train",
         "the quarterly review", "the plumber", "your sister's visit", "the book club", "the tax return",
         "the car service", "the recital", "the board meeting", "the parcel from Leeds"]
ORD = ["first", "second", "third", "fourth", "fifth", "sixth", "seventh", "eighth", "ninth", "tenth",
       "eleventh", "twelfth", "thirteenth", "fourteenth", "fifteenth", "sixteenth", "seventeenth",
       "eighteenth", "nineteenth", "twentieth", "twenty-first", "twenty-second", "twenty-third",
       "twenty-fourth", "twenty-fifth", "twenty-sixth", "twenty-seventh", "twenty-eighth"]


def templates(rng):
    out = []
    n = lambda: rng.choice
    for _ in range(2000):
        f, l, c = rng.choice(FIRST), rng.choice(LAST), rng.choice(CITY)
        mo, wd, th = rng.choice(MONTH), rng.choice(WEEKDAY), rng.choice(THING)
        d = rng.randint(1, 28)
        h, m = rng.randint(1, 12), rng.choice([0, 5, 10, 15, 20, 30, 40, 45, 50, 55])
        ampm = rng.choice(["in the morning", "in the afternoon", "in the evening"])
        tm = f"{h} o'clock" if m == 0 else f"{h}:{m:02d}"
        amt = rng.choice([f"{rng.randint(2, 99)} dollars", f"{rng.randint(2, 99)} pounds and {rng.randint(1, 99)} pence",
                          f"{rng.randint(100, 9999):,} dollars", f"{rng.randint(1, 99)}.{rng.randint(10, 99)} euros"])
        pct = rng.randint(2, 98)
        temp = rng.randint(-5, 38)
        yr = rng.randint(1890, 2031)
        out += [
            f"You have a call from {f} {l} at {tm} {ampm}.",
            f"{th.capitalize()} is scheduled for {wd}, the {ORD[d-1]} of {mo}.",
            f"Shall I tell {f} {l} that you'll be late?",
            f"{f} {l} has just arrived from {c}.",
            f"Your flight to {c} departs at {tm} {ampm} on {wd}.",
            f"The total comes to {amt}, including tax.",
            f"The battery is at {pct} percent.",
            f"It is currently {temp} degrees in {c}.",
            f"Was it in {yr} that {f} {l} moved to {c}?",
            f"I've booked a table for {rng.randint(2, 12)} at {tm} {ampm} on {mo} {d}.",
            f"Would you like me to move {th} to {wd}?",
            f"Please say hello to {f} {l} for me!",
            f"On {mo} {d}, {yr}, {f} {l} wrote to us from {c}.",
            f"There are {rng.randint(13, 9999):,} unread messages, and {rng.randint(2, 12)} of them look important.",
            f"How long has {f} been in {c}?",
            f"What a surprise to see {f} {l} in {c}!",
            f"Call {f} on {rng.randint(200, 999)} {rng.randint(100, 999)} {rng.randint(1000, 9999)}, please.",
        ]
    rng.shuffle(out)
    return out


def words(s):
    return len(s.split())


def grams(s):
    t = re.sub(r"[^a-z ]", "", s.lower())
    return {t[i:i + 3] for i in range(len(t) - 2)}


def greedy(cands, k, rng):
    """Pick k sentences maximising new letter-trigram coverage, ties broken randomly."""
    covered, chosen = set(), []
    pool = [(s, grams(s)) for s in cands]
    rng.shuffle(pool)
    while pool and len(chosen) < k:
        best = max(range(len(pool)), key=lambda i: len(pool[i][1] - covered) / (words(pool[i][0]) ** 0.5))
        s, g = pool.pop(best)
        covered |= g
        chosen.append(s)
    return chosen


def fetch(url, path):
    if not path.exists():
        req = urllib.request.Request(url, headers={"User-Agent": "footman-dataset-build"})
        path.write_bytes(urllib.request.urlopen(req, timeout=60).read())
    return path.read_text(encoding="utf-8", errors="replace")


def ok(s):
    return MIN_W <= words(s) <= MAX_W and "  " not in s and re.search(r"[.?!]$", s)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--cache", default=str(Path(__file__).parent / ".cache"))
    ap.add_argument("--out", default=str(Path(__file__).parent / "script.tsv"))
    a = ap.parse_args()
    cache = Path(a.cache)
    cache.mkdir(exist_ok=True)
    rng = random.Random(9)

    harvard = [html.unescape(m).strip() for m in re.findall(r"<li>(.*?)</li>", fetch(HARVARD_URL, cache / "harvard.html"), re.S)]
    harvard = [re.sub(r"\s+", " ", s) for s in harvard]
    arctic = re.findall(r'"(.*)"', fetch(ARCTIC_URL, cache / "cmuarctic.data"))
    arctic = [s.strip() for s in arctic]

    seen, rows = set(), []

    def add(prefix, items):
        for i, s in enumerate(items, 1):
            key = s.lower()
            if key in seen or not ok(s):
                continue
            seen.add(key)
            rows.append((f"{prefix}-{len(rows)+1:04d}", s, prefix))

    assistant = [s for s in ASSISTANT]
    # short assistant phrases are intentionally allowed under 4 words
    for s in assistant:
        if s.lower() not in seen and 1 <= words(s) <= MAX_W:
            seen.add(s.lower())
            rows.append((f"own-{len(rows)+1:04d}", s, "own-assistant"))
    tmpl = [s for s in templates(rng) if ok(s)]
    add("own", tmpl[: N_OWN - len(rows)])
    for r in rows:  # label by origin
        pass
    add("harvard", greedy([s for s in harvard if ok(s)], N_HARVARD, rng))
    add("arctic", greedy([s for s in arctic if ok(s)], N_ARCTIC, rng))
    src = {"own": "own (CC0, template+handwritten)", "own-assistant": "own (CC0, handwritten)",
           "harvard": "Harvard sentences (IEEE 1969)", "arctic": "CMU ARCTIC prompts (Gutenberg PD text)"}
    with open(a.out, "w", encoding="utf-8") as f:
        f.write("id\ttext\tsource\n")
        for i, (_, text, p) in enumerate(rows, 1):
            f.write(f"ash-{i:04d}\t{text}\t{src[p]}\n")
    print(f"{len(rows)} sentences -> {a.out}")


if __name__ == "__main__":
    main()
