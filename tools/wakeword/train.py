"""Trains the "Hey Athena" wake word on generated speech (generate.py) mixed with noise.

    python train.py <models dir> <data dir> <out dir>

The data dir holds positive/, near/ and speech/ from generate.py. Each clip is placed in three
seconds of background (road noise, music-like tones, other voices, hiss) with some room echo,
streamed through the same front end the device runs (features.py), and cut into the 16-step
windows the model sees. A window of a positive clip counts when it ends just as the wake word
does; every window of the others, and the background before a positive one, counts against.
Prints how many held-out wake words it catches and how often it wakes on held-out speech.
"""
import glob
import math
import os
import random
import sys
from multiprocessing import Pool

import librosa
import numpy as np
import soundfile as sf
import torch
from scipy.signal import fftconvolve, lfilter

sys.path.insert(0, os.path.dirname(__file__))
from features import STEP, Features  # noqa: E402

RATE = 16000
LENGTH = 3 * RATE
WINDOW = 16
EVERY = 2  # keep every other window of the negatives

_features = None
_speech: list[str] = []


def load(path: str) -> np.ndarray:
    d, sr = sf.read(path, dtype="float32")
    return librosa.resample(d, orig_sr=sr, target_sr=RATE) if sr != RATE else d


def trim(d: np.ndarray) -> np.ndarray:
    """The clip without the generator's silence around it."""
    loud = np.flatnonzero(np.abs(d) > 0.02 * np.abs(d).max())
    return d[loud[0]:loud[-1] + 1] if len(loud) else d


def background(rng: np.random.Generator, n: int) -> np.ndarray:
    kind = rng.choice(["white", "pink", "car", "car", "music", "music", "babble", "quiet"])
    if kind == "white":
        x = rng.normal(0, 1, n)
    elif kind == "pink":
        x = lfilter([0.049922, -0.095993, 0.050613, -0.004408], [1, -2.494956, 2.017265, -0.522189],
                    rng.normal(0, 1, n))
    elif kind == "car":  # rumble, engine harmonics and tyre hiss
        rumble = lfilter([1], [1, -0.995], rng.normal(0, 1, n))
        t = np.arange(n) / RATE
        f0 = rng.uniform(25, 60) * (1 + 0.05 * np.sin(2 * np.pi * rng.uniform(0.1, 0.5) * t))
        phase = 2 * np.pi * np.cumsum(f0) / RATE
        engine = sum(np.sin(k * phase) / k for k in range(1, 6))
        hiss = lfilter([1, -0.9], [1], rng.normal(0, 1, n))
        x = rumble / (rumble.std() + 1e-9) + rng.uniform(0.2, 1) * engine + rng.uniform(0.05, 0.4) * hiss
    elif kind == "music":  # notes with harmonics, and a beat
        x = np.zeros(n)
        beat = int(RATE * 60 / rng.uniform(70, 150))
        pos = 0
        while pos < n:
            dur = beat * rng.choice([1, 1, 2])
            for _ in range(rng.integers(1, 4)):
                f = 110 * 2 ** (rng.integers(0, 36) / 12)
                t = np.arange(min(dur, n - pos)) / RATE
                env = np.exp(-t * rng.uniform(2, 8))
                x[pos:pos + len(t)] += env * sum(np.sin(2 * np.pi * f * k * t) / k ** 1.5 for k in range(1, 5))
            if rng.random() < 0.7:
                m = min(int(0.08 * RATE), n - pos)
                x[pos:pos + m] += rng.normal(0, 1, m) * np.exp(-np.arange(m) / (0.02 * RATE))
            pos += dur
        if rng.random() < 0.5 and _speech:  # a singer, or a radio voice
            v = load(random.choice(_speech))
            x[:len(v[:n])] += v[:n] * 3
    elif kind == "babble":
        x = np.zeros(n)
        for _ in range(rng.integers(2, 6)):
            v = load(random.choice(_speech))[:n]
            at = rng.integers(0, max(1, n - len(v)))
            x[at:at + len(v)] += v
    else:
        x = rng.normal(0, 0.05, n)
    return x / (np.sqrt(np.mean(x ** 2)) + 1e-9)


def echo(rng: np.random.Generator, x: np.ndarray) -> np.ndarray:
    if rng.random() < 0.4:
        return x
    rt60 = rng.uniform(0.1, 0.6)
    n = int(rt60 * RATE)
    rir = rng.normal(0, 1, n) * np.exp(-6.9 * np.arange(n) / n)
    rir[0] = 1 / rng.uniform(0.05, 0.5)
    y = fftconvolve(x, rir)[:len(x)]
    return y / (np.abs(y).max() + 1e-9) * np.abs(x).max()


def mix(rng: np.random.Generator, clip: np.ndarray | None) -> tuple[np.ndarray, int, int]:
    """Three seconds of background with the clip in it; where the clip starts and ends."""
    out = background(rng, LENGTH) * 10 ** (rng.uniform(-60, -25) / 20)
    start = end = -1
    if clip is not None:
        clip = echo(rng, trim(clip))[: LENGTH - RATE // 2]
        end = int(rng.uniform(1.9, 2.95) * RATE)
        end = min(end, LENGTH)
        start = max(0, end - len(clip))
        clip = clip[len(clip) - (end - start):]
        speech_rms = np.sqrt(np.mean(clip ** 2)) + 1e-9
        level = 10 ** (rng.uniform(-35, -12) / 20)
        bg_rms = np.sqrt(np.mean(out ** 2)) + 1e-9
        snr = rng.uniform(0, 30)
        out = out / bg_rms * level * 10 ** (-snr / 20)
        out[start:end] += clip / speech_rms * level
    out *= 10 ** (rng.uniform(-6, 6) / 20)
    return (np.clip(out, -1, 1) * 32767).astype(np.int16), start, end


def _init(models: str, speech: list[str]) -> None:
    global _features, _speech
    _features, _speech = Features(models), speech
    torch.set_num_threads(1)


def windows(job: tuple[str | None, bool, int]) -> tuple[np.ndarray, np.ndarray]:
    path, positive, seed = job
    rng = np.random.default_rng(seed)
    random.seed(seed)
    audio, start, end = mix(rng, load(path) if path else None)
    emb = _features.stream(audio)
    xs, ys = [], []
    first = math.ceil(end / STEP) - 1 if positive else None  # the first step that has all of it
    for j in range(WINDOW - 1, len(emb)):
        if positive:
            if first <= j <= first + 2:
                y = 1
            elif (j + 1) * STEP <= start:
                y = 0  # the background before it
            else:
                continue
        elif j % EVERY:
            continue  # neighbouring steps are nearly the same; every other one is plenty
        else:
            y = 0
        xs.append(emb[j - WINDOW + 1:j + 1])
        ys.append(y)
    # Half precision, to fit hundreds of thousands of windows in memory.
    return np.array(xs, dtype=np.float16).reshape(-1, WINDOW, 96), np.array(ys, dtype=np.float32)


def build(models: str, jobs: list, speech: list[str], whole: bool = True):
    with Pool(os.cpu_count(), initializer=_init, initargs=(models, speech)) as pool:
        parts = pool.map(windows, jobs, chunksize=16)
    if not whole:
        return parts
    return np.concatenate([p[0] for p in parts]), np.concatenate([p[1] for p in parts])


class Head(torch.nn.Module):
    def __init__(self, dim: int = 128):
        super().__init__()
        self.model = torch.nn.Sequential(
            torch.nn.Flatten(), torch.nn.Linear(WINDOW * 96, dim), torch.nn.LayerNorm(dim), torch.nn.ReLU(),
            torch.nn.Linear(dim, dim), torch.nn.LayerNorm(dim), torch.nn.ReLU(), torch.nn.Linear(dim, 1),
            torch.nn.Sigmoid())

    def forward(self, x):
        return self.model(x)


def train(x: np.ndarray, y: np.ndarray, steps: int = 12000) -> Head:
    torch.manual_seed(0)
    net = Head()
    opt = torch.optim.Adam(net.parameters(), lr=1e-3)
    X, Y = torch.from_numpy(x), torch.from_numpy(y)
    pos, neg = torch.nonzero(Y == 1).squeeze(1), torch.nonzero(Y == 0).squeeze(1)
    for step in range(steps):
        # Balanced batches; misses on the negatives weigh more as training goes on, as
        # openWakeWord does, to bring false wakes down once the wake word is learned.
        idx = torch.cat([pos[torch.randint(len(pos), (256,))], neg[torch.randint(len(neg), (768,))]])
        p = net(X[idx].float()).squeeze(1)
        w = torch.where(Y[idx] == 1, 1.0, 1.0 + 19.0 * step / steps)
        loss = torch.nn.functional.binary_cross_entropy(p, Y[idx], weight=w)
        opt.zero_grad()
        loss.backward()
        opt.step()
        for g in opt.param_groups:
            g["lr"] = 1e-3 * 0.5 * (1 + math.cos(math.pi * step / steps))
        if step % 2000 == 0:
            print(f"step {step}: loss {loss.item():.4f}", flush=True)
    return net.eval()


def main() -> None:
    models, data, out = sys.argv[1:4]
    random.seed(0)
    sets = {k: sorted(glob.glob(f"{data}/{k}/*.wav")) for k in ("positive", "near", "speech")}
    for v in sets.values():
        random.shuffle(v)
    held = {k: v[: len(v) // 10] for k, v in sets.items()}
    used = {k: v[len(v) // 10:] for k, v in sets.items()}
    speech = used["speech"]
    jobs = [(p, True, i) for i, p in enumerate(used["positive"] * 2)]
    # The others twice over, in different mixes: false wakes cost more than the odd miss.
    jobs += [(p, False, 10 ** 6 + i) for i, p in enumerate((used["near"] + used["speech"]) * 2)]
    jobs += [(None, False, 2 * 10 ** 6 + i) for i in range(int(os.environ.get("WAKE_BG", "6000")))]
    print(f"{len(jobs)} clips to mix", flush=True)
    x, y = build(models, jobs, speech)
    print(f"{int(y.sum())} wake word windows, {int((1 - y).sum())} others", flush=True)
    net = train(x, y, int(os.environ.get("WAKE_STEPS", "20000")))

    # Held out: does it catch the wake word, and how often does it wake on everything else?
    held_pos = build(models, [(p, True, 3 * 10 ** 6 + i) for i, p in enumerate(held["positive"])], speech, whole=False)
    nx, ny = build(models, [(p, False, 4 * 10 ** 6 + i) for i, p in enumerate(held["near"] + held["speech"])]
                   + [(None, False, 5 * 10 ** 6 + i) for i in range(int(os.environ.get("WAKE_BG", "3000")) // 6)],
                   speech)
    with torch.no_grad():
        best = [net(torch.tensor(x[y == 1]).float()).max().item() if (y == 1).any() else 0.0 for x, y in held_pos]
        npred = net(torch.tensor(nx).float()).squeeze(1).numpy()
    hours = 2 * len(nx) * STEP / RATE / 3600  # each window stands for two 80 ms steps of listening
    for t in (0.3, 0.5, 0.7, 0.8, 0.9, 0.95):
        print(f"threshold {t}: catches {np.mean(np.array(best) >= t):.1%} of held-out wake words; "
              f"{(npred >= t).sum() / hours:.1f} wakes an hour on held-out speech and noise "
              f"({(npred >= t).sum()} of {len(npred)} steps)")

    os.makedirs(out, exist_ok=True)
    np.savez(f"{out}/hey_athena.npz", **{k: v.numpy() for k, v in net.state_dict().items()})
    print("wrote", f"{out}/hey_athena.npz")


if __name__ == "__main__":
    main()
