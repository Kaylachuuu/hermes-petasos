"""Spoken clips for training the wake word, from piper-sample-generator's many-voice model.

    python generate.py <piper-sample-generator dir> <out dir> <set> <count> [texts file]

Sets: "positive" says the wake word, "near" says things that sound like it (and "Athena" on its
own, so talking about her doesn't wake her), "speech" says the lines of a texts file.
"""
import random
import sys
from pathlib import Path

WAKE = ["hey athena", "hey, athena", "hey athena!", "hey athena.", "hey athena?"]
NEAR = ["athena", "athena.", "hey anna", "hey tina", "hey there", "hey athens", "hey ethan", "hey alexa",
        "hey siri", "hey jarvis", "hey, Anita", "the arena", "a theater", "hey, then a", "hey, what's the news",
        "hey, I mean", "Athena is great", "I told Athena", "ask Athena later", "athletes", "hey Adina",
        "hey Edina", "hey Selena", "hey Tia", "hey Athos", "Pasadena", "hey Gina", "okay Athena", "hey Mina",
        "say Athena", "hey, a tuna", "hey Thena", "hey, at Ena", "Hellena", "hey Helena", "hey Atlanta",
        "anything", "a thinner", "hey Nina", "hey, that's enough"]

if __name__ == "__main__":
    psg, out, which, count = sys.argv[1], Path(sys.argv[2]), sys.argv[3], int(sys.argv[4])
    sys.path.insert(0, psg)
    from piper_sample_generator.__main__ import generate_samples
    if which == "positive":
        texts = WAKE
    elif which == "near":
        texts = NEAR
    else:
        texts = [t.strip() for t in open(sys.argv[5]) if t.strip()]
    random.seed(hash(which) & 0xffff)
    texts = [random.choice(texts) for _ in range(count)]
    out.mkdir(parents=True, exist_ok=True)
    generate_samples(text=texts, output_dir=out, model=f"{psg}/models/en_US-libritts_r-medium.pt",
                     max_samples=count, batch_size=32, max_speakers=900,
                     length_scales=(0.8, 0.9, 1.0, 1.1, 1.25), noise_scales=(0.5, 0.667, 0.8),
                     noise_scale_ws=(0.6, 0.8, 1.0), slerp_weights=(0.3, 0.5, 0.7))
