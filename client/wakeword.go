package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"math/cmplx"
)

// The wake word ("Hey Athena"): openWakeWord's way of spotting a phrase, run here in plain Go so
// the Car Thing needs nothing installed. Audio at 16 kHz goes in 80 ms (1280 sample) steps; each
// step gives 8 frames of a 32-band mel spectrogram, then one 96-number embedding of the last 76
// frames (Google's speech_embedding network), and the wake word model scores the last 16
// embeddings, about 1.3 seconds. tools/wakeword has the reference this matches and the training.
//
// The embedding network is mostly convolutions along time with no padding there, and every
// pooling step along time divides by two, three times over: 8 frames, the step, is exactly one
// row at the top. So rather than run the whole 76-frame window every step, each layer keeps
// the few rows of history it needs and works out only its new rows: about a ninth of the work,
// with the same result.

const (
	wakeRate   = 16000
	wakeStep   = 1280 // samples per step, 80 ms
	wakeChunk  = 1760 // samples the mel frames of a step are taken from
	wakeFFT    = 512
	wakeHop    = 160
	wakeBands  = 32
	wakeWindow = 76 // mel frames per embedding
	wakeEmbed  = 96
	wakeFrames = 16 // embeddings the wake word model sees
)

type wakeModel struct {
	window []float32 // the STFT window, wakeFFT long
	melW   []float32 // 257 x 32
	convs  []*wakeConv
	fc     [3]wakeDense
	ln     [2]wakeNorm
}

type wakeConv struct {
	out, in, kh, kw int
	w, b            []float32 // out x in x kh x kw; b may be empty (the last layer)
	act             bool      // LeakyReLU(0.2) and then max with -0.4, as the network has
	poolT, poolF    int       // the max-pool after it: 1 or 2 along time and frequency (1, 1: none)
}

type wakeDense struct {
	out, in int
	w, b    []float32 // out x in
}

type wakeNorm struct{ w, b []float32 }

// loadWakeModel reads a wake word written by tools/wakeword/export.py.
func loadWakeModel(data []byte) (*wakeModel, error) {
	r := bytes.NewReader(data)
	var magic [4]byte
	var count uint32
	if _, err := io.ReadFull(r, magic[:]); err != nil || string(magic[:]) != "PWW1" {
		return nil, fmt.Errorf("not a wake word file")
	}
	if err := binary.Read(r, binary.LittleEndian, &count); err != nil {
		return nil, err
	}
	type tensor struct {
		dims []int
		data []float32
	}
	ts := map[string]tensor{}
	for i := 0; i < int(count); i++ {
		var n, nd uint8
		if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
			return nil, err
		}
		name := make([]byte, n)
		if _, err := io.ReadFull(r, name); err != nil {
			return nil, err
		}
		if err := binary.Read(r, binary.LittleEndian, &nd); err != nil {
			return nil, err
		}
		dims := make([]uint32, nd)
		if err := binary.Read(r, binary.LittleEndian, dims); err != nil {
			return nil, err
		}
		size := 1
		t := tensor{}
		for _, d := range dims {
			size *= int(d)
			t.dims = append(t.dims, int(d))
		}
		t.data = make([]float32, size)
		if err := binary.Read(r, binary.LittleEndian, t.data); err != nil {
			return nil, err
		}
		ts[string(name)] = t
	}
	need := func(name string, size int) ([]float32, error) {
		t, ok := ts[name]
		if !ok || (size > 0 && len(t.data) != size) {
			return nil, fmt.Errorf("wake word file: %s missing or the wrong size", name)
		}
		return t.data, nil
	}
	m := &wakeModel{}
	var err error
	if m.window, err = need("window", wakeFFT); err != nil {
		return nil, err
	}
	if m.melW, err = need("melW", (wakeFFT/2+1)*wakeBands); err != nil {
		return nil, err
	}
	// The embedding network's shape: where it pools, after which convolutions.
	pools := map[int][2]int{2: {2, 2}, 6: {1, 2}, 10: {2, 2}, 14: {1, 2}, 18: {2, 2}}
	for i := 0; ; i++ {
		t, ok := ts[fmt.Sprintf("conv%d.w", i)]
		if !ok {
			break
		}
		if len(t.dims) != 4 {
			return nil, fmt.Errorf("wake word file: conv%d is not 4-D", i)
		}
		c := &wakeConv{out: t.dims[0], in: t.dims[1], kh: t.dims[2], kw: t.dims[3], w: t.data, poolT: 1, poolF: 1}
		if b, ok := ts[fmt.Sprintf("conv%d.b", i)]; ok {
			c.b, c.act = b.data, true
		}
		if p, ok := pools[i]; ok {
			c.poolT, c.poolF = p[0], p[1]
		}
		m.convs = append(m.convs, c)
	}
	if len(m.convs) != 20 {
		return nil, fmt.Errorf("wake word file: %d convolutions, not 20", len(m.convs))
	}
	for i := range m.fc {
		t, ok := ts[fmt.Sprintf("fc%d.w", i)]
		if !ok || len(t.dims) != 2 {
			return nil, fmt.Errorf("wake word file: fc%d missing", i)
		}
		m.fc[i] = wakeDense{out: t.dims[0], in: t.dims[1], w: t.data}
		if m.fc[i].b, err = need(fmt.Sprintf("fc%d.b", i), t.dims[0]); err != nil {
			return nil, err
		}
	}
	if m.fc[0].in != wakeFrames*wakeEmbed || m.fc[2].out != 1 {
		return nil, fmt.Errorf("wake word file: the model doesn't take %d x %d", wakeFrames, wakeEmbed)
	}
	for i := range m.ln {
		if m.ln[i].w, err = need(fmt.Sprintf("ln%d.w", i), m.fc[i].out); err != nil {
			return nil, err
		}
		if m.ln[i].b, err = need(fmt.Sprintf("ln%d.b", i), m.fc[i].out); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// wakeSpotter listens to one stream of audio for the wake word.
type wakeSpotter struct {
	m       *wakeModel
	audio   []int16     // the last wakeChunk samples
	pending []int16     // samples toward the next step
	layers  []*wakeRows // each layer's input rows still needed
	embeds  [][]float32 // the last wakeFrames embeddings
	fft     []complex128
	twiddle []complex128
}

// wakeRows is a layer's input: the rows it hasn't finished with, and how many rows have come
// in so far (which decides how rows pair up for pooling).
type wakeRows struct {
	rows [][]float32
	seen int
	half []float32 // the first of a pair of rows waiting to be pooled
}

func newWakeSpotter(m *wakeModel) *wakeSpotter {
	s := &wakeSpotter{m: m, audio: make([]int16, wakeChunk), fft: make([]complex128, wakeFFT)}
	s.twiddle = make([]complex128, wakeFFT/2)
	for k := range s.twiddle {
		s.twiddle[k] = cmplx.Exp(complex(0, -2*math.Pi*float64(k)/wakeFFT))
	}
	for range m.convs {
		s.layers = append(s.layers, &wakeRows{})
	}
	// The mel frames start out as ones, as openWakeWord's do.
	ones := make([][]float32, wakeWindow)
	for i := range ones {
		ones[i] = make([]float32, wakeBands)
		for j := range ones[i] {
			ones[i][j] = 1
		}
	}
	s.embed(ones)
	s.embeds = nil
	return s
}

// write takes audio at 16 kHz and returns the wake word's score for each 80 ms step completed,
// from 0 to 1; there are no scores until it has heard enough (about 1.3 seconds).
func (s *wakeSpotter) write(pcm []int16) []float32 {
	var scores []float32
	for len(pcm) > 0 {
		n := min(wakeStep-len(s.pending), len(pcm))
		s.pending = append(s.pending, pcm[:n]...)
		pcm = pcm[n:]
		if len(s.pending) < wakeStep {
			break
		}
		s.audio = append(s.audio[wakeStep:], s.pending...)
		s.pending = s.pending[:0]
		e := s.embed(s.mels())
		s.embeds = append(s.embeds, e)
		if len(s.embeds) > wakeFrames {
			s.embeds = s.embeds[1:]
		}
		if len(s.embeds) == wakeFrames {
			scores = append(scores, s.score())
		}
	}
	return scores
}

// mels is the step's 8 mel frames, scaled as openWakeWord does (x/10 + 2).
func (s *wakeSpotter) mels() [][]float32 {
	m := s.m
	n := (wakeChunk-wakeFFT)/wakeHop + 1
	out := make([][]float32, n)
	power := make([]float64, wakeFFT/2+1)
	top := float32(math.Inf(-1))
	for i := 0; i < n; i++ {
		for j := 0; j < wakeFFT; j++ {
			s.fft[j] = complex(float64(s.audio[i*wakeHop+j])*float64(m.window[j]), 0)
		}
		s.transform()
		for k := range power {
			power[k] = real(s.fft[k])*real(s.fft[k]) + imag(s.fft[k])*imag(s.fft[k])
		}
		frame := make([]float32, wakeBands)
		for b := 0; b < wakeBands; b++ {
			var sum float64
			for k := range power {
				sum += power[k] * float64(m.melW[k*wakeBands+b])
			}
			db := float32(10 * math.Log10(max(sum, 1e-10)))
			frame[b] = db
			top = max(top, db)
		}
		out[i] = frame
	}
	for _, frame := range out { // no quieter than 80 dB below the step's loudest
		for b := range frame {
			frame[b] = max(frame[b], top-80)/10 + 2
		}
	}
	return out
}

// transform is an in-place radix-2 FFT of s.fft.
func (s *wakeSpotter) transform() {
	a := s.fft
	n := len(a)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		step := n / size
		for start := 0; start < n; start += size {
			for k := 0; k < size/2; k++ {
				t := s.twiddle[k*step] * a[start+k+size/2]
				a[start+k+size/2] = a[start+k] - t
				a[start+k] += t
			}
		}
	}
}

// embed runs new mel frames up through the network, returning the newest embedding.
func (s *wakeSpotter) embed(frames [][]float32) []float32 {
	rows := frames
	for i, c := range s.m.convs {
		rows = c.feed(s.layers[i], rows, wakeBands>>shrinkF(s.m.convs[:i]))
	}
	if len(rows) == 0 {
		return nil
	}
	return rows[len(rows)-1]
}

// shrinkF is how many times the frequency axis has been halved before a layer.
func shrinkF(before []*wakeConv) int {
	n := 0
	for _, c := range before {
		if c.poolF == 2 {
			n++
		}
	}
	return n
}

// feed adds rows (each in x width, channel by channel) to a layer and returns its new output
// rows, after its activation and pooling.
func (c *wakeConv) feed(st *wakeRows, in [][]float32, width int) [][]float32 {
	var conv [][]float32
	for _, row := range in {
		st.rows = append(st.rows, row)
		st.seen++
		if len(st.rows) < c.kh {
			continue
		}
		conv = append(conv, c.row(st.rows[len(st.rows)-c.kh:], width))
		st.rows = st.rows[len(st.rows)-c.kh+1:]
	}
	if c.poolT == 1 && c.poolF == 1 {
		return conv
	}
	// Pool: along frequency in pairs, and along time in pairs of rows counted from the first.
	var out [][]float32
	for _, row := range conv {
		row = c.poolFreq(row, width)
		if c.poolT == 1 {
			out = append(out, row)
			continue
		}
		if st.half == nil {
			st.half = row
			continue
		}
		for i := range row {
			row[i] = max(row[i], st.half[i])
		}
		st.half = nil
		out = append(out, row)
	}
	return out
}

// row is one output row of the convolution from the kh input rows it needs: each output
// position's patch of inputs is gathered once, then each output channel is one dot product.
func (c *wakeConv) row(in [][]float32, width int) []float32 {
	out := make([]float32, c.out*width)
	pad := (c.kw - 1) / 2 // the network pads along frequency to keep its width
	k := c.in * c.kh * c.kw
	patch := make([]float32, k)
	for f := 0; f < width; f++ {
		n := 0
		for i := 0; i < c.in; i++ {
			for dt := 0; dt < c.kh; dt++ {
				src := in[dt][i*width : (i+1)*width]
				for df := 0; df < c.kw; df++ {
					if g := f + df - pad; g >= 0 && g < width {
						patch[n] = src[g]
					} else {
						patch[n] = 0
					}
					n++
				}
			}
		}
		for o := 0; o < c.out; o++ {
			v := dot(c.w[o*k:(o+1)*k], patch)
			if len(c.b) > 0 {
				v += c.b[o]
			}
			if c.act {
				if v < 0 {
					v *= 0.2
				}
				v = max(v, -0.4)
			}
			out[o*width+f] = v
		}
	}
	return out
}

// dot is the dot product of two equally long vectors, four at a time.
func dot(a, b []float32) float32 {
	b = b[:len(a)]
	var s0, s1, s2, s3 float32
	i := 0
	for ; i+4 <= len(a); i += 4 {
		s0 += a[i] * b[i]
		s1 += a[i+1] * b[i+1]
		s2 += a[i+2] * b[i+2]
		s3 += a[i+3] * b[i+3]
	}
	for ; i < len(a); i++ {
		s0 += a[i] * b[i]
	}
	return s0 + s1 + s2 + s3
}

func (c *wakeConv) poolFreq(row []float32, width int) []float32 {
	if c.poolF == 1 {
		return row
	}
	half := width / 2
	out := make([]float32, c.out*half)
	for o := 0; o < c.out; o++ {
		for f := 0; f < half; f++ {
			out[o*half+f] = max(row[o*width+2*f], row[o*width+2*f+1])
		}
	}
	return out
}

// score is the wake word model on the last 16 embeddings.
func (s *wakeSpotter) score() float32 {
	x := make([]float32, 0, wakeFrames*wakeEmbed)
	for _, e := range s.embeds {
		x = append(x, e...)
	}
	for i := 0; i < 2; i++ {
		x = s.m.fc[i].apply(x)
		s.m.ln[i].apply(x)
		for j, v := range x {
			x[j] = max(v, 0)
		}
	}
	z := s.m.fc[2].apply(x)[0]
	return float32(1 / (1 + math.Exp(-float64(z))))
}

func (d wakeDense) apply(x []float32) []float32 {
	out := make([]float32, d.out)
	for o := range out {
		sum := d.b[o]
		for i, w := range d.w[o*d.in : (o+1)*d.in] {
			sum += w * x[i]
		}
		out[o] = sum
	}
	return out
}

func (n wakeNorm) apply(x []float32) {
	var mean, vr float64
	for _, v := range x {
		mean += float64(v)
	}
	mean /= float64(len(x))
	for _, v := range x {
		vr += (float64(v) - mean) * (float64(v) - mean)
	}
	sd := math.Sqrt(vr/float64(len(x)) + 1e-5)
	for i, v := range x {
		x[i] = float32((float64(v)-mean)/sd)*n.w[i] + n.b[i]
	}
}
