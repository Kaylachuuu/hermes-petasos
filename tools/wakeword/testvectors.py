"""Test vectors for client/wakeword_test.go: a clip, and what the reference makes of it.

    python testvectors.py <models dir> <wake word .onnx> <clip .wav> <out dir>
"""
import json
import sys

import librosa
import numpy as np
import onnxruntime as ort
import soundfile as sf

from features import Features

models, head_path, wav, out = sys.argv[1:5]
d, sr = sf.read(wav)
d = librosa.resample(d, orig_sr=sr, target_sr=16000)
rng = np.random.default_rng(0)
a = np.concatenate([rng.normal(0, 0.003, 24000), d, rng.normal(0, 0.003, 16000)])
audio = (np.clip(a, -1, 1) * 32767).astype(np.int16)
emb = Features(models).stream(audio)
if head_path.endswith(".npz"):
    w = dict(np.load(head_path))

    def score(x):
        x = x.reshape(-1)
        for a, n in (("1", "2"), ("4", "5")):
            x = w[f"model.{a}.weight"] @ x + w[f"model.{a}.bias"]
            x = (x - x.mean()) / np.sqrt(x.var() + 1e-5) * w[f"model.{n}.weight"] + w[f"model.{n}.bias"]
            x = np.maximum(x, 0)
        z = (w["model.7.weight"] @ x + w["model.7.bias"]).item()
        return 1 / (1 + np.exp(-z))
else:
    head = ort.InferenceSession(head_path, providers=["CPUExecutionProvider"])
    name = head.get_inputs()[0].name

    def score(x):
        return head.run(None, {name: x[None]})[0].item()
scores = [score(emb[i - 16:i]) for i in range(16, len(emb) + 1)]
audio.tofile(f"{out}/wake-clip.raw")
json.dump({"embeddings": emb.tolist(), "scores": scores}, open(f"{out}/wake-clip.json", "w"))
print(len(audio), "samples;", len(emb), "embeddings; top score", max(scores))
