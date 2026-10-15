# The "Hey Athena" wake word

`client/wakeword/hey-athena.bin` is made here. Python 3.11 with `torch`, `onnx`, `onnxruntime`,
`librosa`, `soundfile`, `scipy` and `piper-tts`; no GPU needed.

1. openWakeWord's `melspectrogram.onnx` and `embedding_model.onnx` from its v0.5.1 release, in
   a models dir.
2. [piper-sample-generator](https://github.com/rhasspy/piper-sample-generator) with
   `models/en_US-libritts_r-medium.pt` from its releases, for many different voices.
3. Clips (a few thousand of each), then training and export:

```sh
python generate.py psg data/positive positive 6000
python generate.py psg data/near near 6000
python generate.py psg data/speech speech 8000 texts.txt   # any plain sentences, one a line
python train.py models data out                            # prints catch and false-wake rates
python export.py models out/hey_athena.npz ../../client/wakeword/hey-athena.bin
python testvectors.py models out/hey_athena.npz some-clip.wav ../../client/testdata
```

The background noise (road, engine, music-like, babble, hiss, room echo) is made up in
`train.py`, so nothing else needs downloading. Real recordings of other people talking would
cut false wakes further, as negatives in `data/speech`.
