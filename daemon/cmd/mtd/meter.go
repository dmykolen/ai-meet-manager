package main

import (
	"fmt"
	"math"
	"strings"
)

// channel accumulates the level of one channel: peak for "is anything there at
// all", RMS for how loud it actually was.
type channel struct {
	peak    int
	squares float64
	n       int
}

func (c *channel) add(s int16) {
	v := int(s)
	if v < 0 {
		v = -v
	}
	c.peak = max(c.peak, v)
	c.squares += float64(s) * float64(s)
	c.n++
}

func (c channel) rms() float64 {
	if c.n == 0 {
		return 0
	}
	return math.Sqrt(c.squares / float64(c.n))
}

// bar is drawn in decibels rather than raw amplitude. Linear, the meter pegged
// at about 7% of full scale and every voice looked identical.
func bar(label string, c channel) string {
	const width = 18
	filled := 0
	if rms := c.rms(); rms > 1 {
		// -60 dBFS at the left edge, full scale at the right.
		db := 20 * math.Log10(rms/32768)
		filled = min(max(int((db+60)/60*width), 0), width)
	}
	return fmt.Sprintf("%s |%-*s|", label, width, strings.Repeat("#", filled))
}

func report(name string, c channel) {
	verdict := "carrying audio"
	switch {
	case c.peak == 0:
		verdict = "SILENT — nothing was captured"
	case c.rms() < 5:
		verdict = "almost silent — check the device and the permission"
	}
	fmt.Printf("%s  peak %5d (%4.1f%% FS)  rms %6.1f  %s\n",
		name, c.peak, float64(c.peak)/327.68, c.rms(), verdict)
}
