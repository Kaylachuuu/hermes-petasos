"""Writes a wake word for petasos (client/wakeword/*.bin): openWakeWord's front end and a
wake word model, as plain float32 tensors client/wakeword.go reads.

    python export.py <models dir> <wake word .onnx> <out .bin>

The models dir holds openWakeWord's melspectrogram.onnx and embedding_model.onnx (from its
v0.5.1 release); the wake word model is an openWakeWord-style one: 16 x 96 in, Linear, LayerNorm,
ReLU, Linear, LayerNorm, ReLU, Linear, Sigmoid, as ONNX or as train.py's .npz.
"""
import struct
import sys

import numpy as np
import onnx
from onnx import numpy_helper


def tensors(models: str, head: str) -> list[tuple[str, np.ndarray]]:
    out = []
    mel = {i.name: numpy_helper.to_array(i) for i in onnx.load(f"{models}/melspectrogram.onnx").graph.initializer}
    out.append(("window", mel["0.stft.conv_real.weight"][0, 0]))  # the k=0 row of the windowed DFT
    out.append(("melW", mel["1.melW"]))
    emb = onnx.load(f"{models}/embedding_model.onnx")
    init = {i.name: numpy_helper.to_array(i) for i in emb.graph.initializer}
    convs = [n for n in emb.graph.node if n.op_type == "Conv"]
    for i, n in enumerate(convs):
        out.append((f"conv{i}.w", init[n.input[1]]))
        if len(n.input) > 2:
            out.append((f"conv{i}.b", init[n.input[2]]))
    if head.endswith(".npz"):  # train.py's
        h = dict(np.load(head))
    else:
        h = {i.name: numpy_helper.to_array(i) for i in onnx.load(head).graph.initializer
             if i.name.startswith("model.")}  # not a verifier model some carry alongside
    order = lambda k: int(k.split(".")[1])  # noqa: E731  model.<layer>.weight
    gemms = sorted((k for k in h if k.endswith(".weight") and h[k].ndim == 2), key=order)
    norms = sorted((k for k in h if k.endswith(".weight") and h[k].ndim == 1), key=order)
    for i, k in enumerate(gemms):
        out += [(f"fc{i}.w", h[k]), (f"fc{i}.b", h[k.replace('.weight', '.bias')])]
    for i, k in enumerate(norms):
        out += [(f"ln{i}.w", h[k]), (f"ln{i}.b", h[k.replace('.weight', '.bias')])]
    return out


def write(path: str, items: list[tuple[str, np.ndarray]]) -> None:
    with open(path, "wb") as f:
        f.write(b"PWW1" + struct.pack("<I", len(items)))
        for name, arr in items:
            arr = np.ascontiguousarray(arr, dtype="<f4")
            f.write(struct.pack("<B", len(name)) + name.encode() + struct.pack("<B", arr.ndim))
            f.write(struct.pack(f"<{arr.ndim}I", *arr.shape) + arr.tobytes())


if __name__ == "__main__":
    write(sys.argv[3], tensors(sys.argv[1], sys.argv[2]))
