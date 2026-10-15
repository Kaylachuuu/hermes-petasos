"""openWakeWord's front end, as petasos runs it (client/wakeword.go), for training and checking.

The audio is 16 kHz 16-bit mono. Every 80 ms (1280 samples) the mel spectrogram of the last
1760 samples gives 8 new frames; each 80 ms step then gives one 96-number embedding of the last
76 frames (Google's speech_embedding, through openWakeWord's ONNX models), and a wake word
model looks at the last 16 embeddings. This streams the way the device does, so the model is
trained on the features it will see.
"""
import numpy as np
import onnxruntime as ort

STEP = 1280


class Features:
    def __init__(self, models_dir: str):
        opts = ort.SessionOptions()
        opts.intra_op_num_threads = 1
        self.mel = ort.InferenceSession(f"{models_dir}/melspectrogram.onnx", opts, providers=["CPUExecutionProvider"])
        self.emb = ort.InferenceSession(f"{models_dir}/embedding_model.onnx", opts, providers=["CPUExecutionProvider"])

    def mels(self, chunk: np.ndarray) -> np.ndarray:
        out = self.mel.run(None, {"input": chunk.astype(np.float32)[None]})[0]
        return np.squeeze(out) / 10 + 2

    def stream(self, audio: np.ndarray) -> np.ndarray:
        """The embeddings for a clip of int16 audio, one per 80 ms step, as the device makes them.
        The mel frames start out as ones, as on the device (and in openWakeWord)."""
        audio = np.asarray(audio, dtype=np.int16)
        padded = np.concatenate([np.zeros(1760 - STEP, dtype=np.int16), audio])
        frames = [np.ones((76, 32), dtype=np.float32)]
        for end in range(STEP, len(audio) + 1, STEP):
            frames.append(self.mels(padded[end - STEP:end - STEP + 1760]))
        frames = np.vstack(frames)
        steps = (len(frames) - 76) // 8
        if steps <= 0:
            return np.zeros((0, 96), dtype=np.float32)
        windows = np.stack([frames[8 * (i + 1):8 * (i + 1) + 76] for i in range(steps)])
        return self.emb.run(None, {"input_1": windows[..., None].astype(np.float32)})[0].reshape(-1, 96)
