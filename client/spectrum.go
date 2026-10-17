package main

import (
	"encoding/binary"
	"math"
	"math/cmplx"
)

// The sound's level in a few bands of pitch, for the top meander: from the bass to the treble.

// bandEdges are the bands' bounds in Hz, for each number of bands the Device tab offers.
var bandEdges = map[int][]float64{
	3: {30, 250, 2000, 16000},
	4: {30, 250, 1000, 4000, 16000},
	5: {30, 150, 500, 2000, 6000, 16000},
}

// pcmBands is how loud each band of 16-bit samples is, 0 to 1, on the decibel scale pcmLevel
// uses. Each band's power is taken per octave, so pink noise (which most music roughly follows)
// lights them all alike, whatever their widths.
func pcmBands(pcm []byte, rate, channels, count int) []float64 {
	edges := bandEdges[count]
	if edges == nil {
		return nil
	}
	out := make([]float64, count)
	frames := len(pcm) / (2 * channels)
	n := 1
	for n*2 <= frames {
		n *= 2
	}
	if n < 64 || rate <= 0 {
		return out
	}
	x := make([]complex128, n)
	var wsum float64
	for i := range n { // the channels mixed down, under a Hann window
		var s float64
		for c := range channels {
			s += float64(int16(binary.LittleEndian.Uint16(pcm[2*(i*channels+c):])))
		}
		w := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n-1))
		wsum += w * w
		x[i] = complex(s/float64(channels)/32768*w, 0)
	}
	fft(x)
	power := make([]float64, count)
	hz := float64(rate) / float64(n)
	for k := 1; k < n/2; k++ {
		f := float64(k) * hz
		for b := range count {
			if f >= edges[b] && f < edges[b+1] {
				a := cmplx.Abs(x[k])
				power[b] += 2 * a * a / (wsum * float64(n)) // the band's share of the mean square
				break
			}
		}
	}
	for b := range count {
		octaves := math.Log2(math.Min(edges[b+1], float64(rate)/2) / edges[b])
		if power[b] <= 0 || octaves <= 0 {
			continue
		}
		// A band of pink noise holds octaves/9 of its power (30 Hz to 16 kHz is 9 octaves):
		// scaled back up, so each band reads as the whole sound would at that loudness.
		db := 10 * math.Log10(power[b]/octaves*9)
		out[b] = math.Max(0, math.Min(1, (db+50)/50))
	}
	return out
}

// fft is an in-place radix-2 FFT; len(x) is a power of two.
func fft(x []complex128) {
	n := len(x)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j |= bit
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		step := cmplx.Exp(complex(0, -2*math.Pi/float64(size)))
		for start := 0; start < n; start += size {
			w := complex(1, 0)
			for k := range size / 2 {
				a, b := x[start+k], x[start+k+size/2]*w
				x[start+k], x[start+k+size/2] = a+b, a-b
				w *= step
			}
		}
	}
}
