package modules

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ffnvr/internal/config"
)

// The motion detector of the archive's bars (/system/motion-detector). Three methods,
// the fastest first:
//
//	packets    the sizes of the video packets, nothing decoded (VBR, smart codecs); every
//	           range found is checked in the picture (2 s decoded), false ones go
//	keyframes  the picture of the key frames only (a short GOP)
//	pixels     the whole picture (slow, works always)
//
// mode=auto takes packets when their sizes follow the motion, keyframes else;
// mode=heuristic chooses the same way among the methods given; mode=manual takes the
// one method given.
//
//	/system/motion-detector
//	set mode=heuristic methods=packets,pixels
//	set mode=manual methods=keyframes pixel-fps=2

// The methods.
const (
	MethodPackets   = "packets"
	MethodKeyframes = "keyframes"
	MethodPixels    = "pixels"
)

var motionMethods = []string{MethodPackets, MethodKeyframes, MethodPixels}

// MotionDetector holds the settings.
type MotionDetector struct {
	Mode    string   // auto, heuristic, manual
	Methods []string // heuristic: the methods it may choose; manual: the one
	// ranges
	Gap         float64 // a pause (s) that ends a range
	MinDuration float64 // the shortest event (s)
	Padding     float64 // added before and after a range (s)
	Skip        float64 // the start of a segment not looked at (s)
	// packets
	Window    int     // the moving median of the background: this many seconds each side
	RatioMin  float64 // the least packet signal (× background) that counts
	NoiseK    float64 // threshold = 1 + noise-k × noise (when that is higher)
	NoiseKLow float64 // the lower threshold that widens ranges (hysteresis)
	NoiseMax  float64 // more noise than this: the packet signal is not to be trusted
	CBRFlat   float64 // the ratio of 1 s noise to ~10 s noise above which it is CBR
	MaxVerify int     // how many packet ranges are checked in the picture at most
	// pixels
	PixelDiff       int     // a change of a pixel's brightness (0-255) that is motion
	PixelMinPercent float64 // that much of the picture changed (%) is motion
	PixelFPS        float64 // frames a second looked at by the pixels method
	PixelWidth      int     // the width the picture is scaled to
	OSDHeight       int     // the band at the top left out (the clock), after scaling
	GlobalPercent   float64 // more of the picture at once (%): light, IR switching, not motion
	KeyframeMaxGOP  float64 // a GOP up to this (s): the key frames are enough
}

// DefaultMotionDetector: the defaults of every setting.
var DefaultMotionDetector = MotionDetector{
	Mode: "auto", Gap: 3, MinDuration: 1, Padding: 2, Skip: 4,
	Window: 60, RatioMin: 1.12, NoiseK: 4, NoiseKLow: 2, NoiseMax: 0.30, CBRFlat: 6, MaxVerify: 30,
	PixelDiff: 20, PixelMinPercent: 0.10, PixelFPS: 4, PixelWidth: 320, OSDHeight: 24, GlobalPercent: 50, KeyframeMaxGOP: 5,
}

type mdNum struct {
	name     string
	min, max float64
	integer  bool
	seconds  bool // a length: 3s, 500ms, 3
	help     string
	ptr      func(*MotionDetector) any // *float64 or *int
}

var motionNums = []mdNum{
	{"gap", 0.1, 300, false, true, "a pause that ends a range (default 3s)", func(d *MotionDetector) any { return &d.Gap }},
	{"min-duration", 0, 300, false, true, "the shortest event (default 1s)", func(d *MotionDetector) any { return &d.MinDuration }},
	{"padding", 0, 300, false, true, "added before and after every range (default 2s)", func(d *MotionDetector) any { return &d.Padding }},
	{"skip", 0, 300, false, true, "the start of every segment not looked at (false motion as the stream starts); default 4s, 0s: none", func(d *MotionDetector) any { return &d.Skip }},
	{"window", 1, 300, true, true, "packets: the moving median of the background, seconds each side (default 60s)", func(d *MotionDetector) any { return &d.Window }},
	{"ratio-min", 1, 10, false, false, "packets: the least signal (× background) that counts (default 1.12)", func(d *MotionDetector) any { return &d.RatioMin }},
	{"noise-k", 0, 100, false, false, "packets: threshold = 1 + noise-k × noise, when higher than ratio-min (default 4)", func(d *MotionDetector) any { return &d.NoiseK }},
	{"noise-k-low", 0, 100, false, false, "packets: the lower threshold that widens ranges (hysteresis, default 2)", func(d *MotionDetector) any { return &d.NoiseKLow }},
	{"noise-max", 0, 10, false, false, "packets: more noise than this and the packet sizes are not trusted (default 0.30)", func(d *MotionDetector) any { return &d.NoiseMax }},
	{"cbr-flat", 0.1, 1000, false, false, "packets: 1 s noise / ~10 s noise above which the stream is CBR (default 6)", func(d *MotionDetector) any { return &d.CBRFlat }},
	{"max-verify", 0, 1000, true, false, "packets: how many ranges are checked in the picture at most (default 30)", func(d *MotionDetector) any { return &d.MaxVerify }},
	{"pixel-diff", 1, 254, true, false, "pixels, keyframes: a change of a pixel's brightness (0-255) that is motion (default 20)", func(d *MotionDetector) any { return &d.PixelDiff }},
	{"pixel-min-percent", 0, 100, false, false, "pixels, keyframes: that much of the picture changed (%) is motion (default 0.10)", func(d *MotionDetector) any { return &d.PixelMinPercent }},
	{"pixel-fps", 0.1, 60, false, false, "pixels: frames a second looked at (default 4)", func(d *MotionDetector) any { return &d.PixelFPS }},
	{"pixel-width", 32, 3840, true, false, "pixels, keyframes: the width the picture is scaled to (default 320)", func(d *MotionDetector) any { return &d.PixelWidth }},
	{"osd-height", 0, 1000, true, false, "pixels, keyframes: the band at the top left out, the clock (px after scaling, default 24)", func(d *MotionDetector) any { return &d.OSDHeight }},
	{"global-percent", 1, 100, false, false, "pixels, keyframes: more of the picture at once (%) is light or IR switching, not motion (default 50)", func(d *MotionDetector) any { return &d.GlobalPercent }},
	{"keyframe-max-gop", 0.1, 300, false, true, "a GOP up to this is short enough for the keyframes method (default 5s)", func(d *MotionDetector) any { return &d.KeyframeMaxGOP }},
}

func motionDetectorMenu() *config.Menu {
	fields := []config.Field{
		{Name: "mode", Type: config.TEnum, Enum: []string{"auto", "heuristic", "manual"},
			Help: "auto: packets, or keyframes when the packets do not follow motion; heuristic: the same choice among methods=; manual: the one of methods= (default auto)"},
		{Name: "methods", Type: config.TEnumList, Enum: motionMethods,
			Help: "mode=heuristic: the methods it may choose (e.g. packets,pixels); mode=manual: the one method"},
	}
	for _, n := range motionNums {
		fields = append(fields, config.Field{Name: n.name, Help: n.help})
	}
	return &config.Menu{
		Path:   "/system/motion-detector",
		Kind:   config.Single,
		Help:   "how the motion bars of the archive are made (local recordings)",
		Fields: fields,
		Validate: func(_ *config.Store, it *config.Item) error {
			_, err := readMotionDetector(it, true)
			return err
		},
	}
}

// readMotionDetector reads the settings; normalise: tidy the values in the item.
func readMotionDetector(it *config.Item, normalise bool) (MotionDetector, error) {
	d := DefaultMotionDetector
	d.Mode = or(it.Val("mode"), "auto")
	seen := map[string]bool{}
	for _, m := range splitList(it.Val("methods")) {
		if !seen[m] {
			seen[m] = true
			d.Methods = append(d.Methods, m)
		}
	}
	switch {
	case d.Mode == "auto" && len(d.Methods) > 0:
		return d, errors.New("methods: only with mode=heuristic or mode=manual (auto chooses packets or keyframes itself); /system/motion-detector unset methods")
	case d.Mode == "heuristic" && len(d.Methods) == 0:
		return d, errors.New("mode=heuristic needs methods=, the ones it may choose: e.g. methods=packets,keyframes")
	case d.Mode == "manual" && len(d.Methods) != 1:
		return d, errors.New("mode=manual needs exactly one of methods=packets, keyframes or pixels")
	}
	if normalise && len(d.Methods) > 0 {
		it.Set("methods", strings.Join(d.Methods, ","))
	}
	for _, n := range motionNums {
		v := it.Val(n.name)
		if v == "" {
			continue
		}
		var f float64
		if n.seconds {
			dur, err := parseSeconds(v)
			if err != nil {
				return d, fmt.Errorf("%s %w", n.name, err)
			}
			f = dur.Seconds()
		} else {
			var err error
			if f, err = strconv.ParseFloat(v, 64); err != nil || math.IsNaN(f) {
				return d, fmt.Errorf("%s %q: a number", n.name, v)
			}
		}
		if n.integer && f != math.Trunc(f) {
			return d, fmt.Errorf("%s %q: a whole number", n.name, v)
		}
		if f < n.min || f > n.max {
			unit := ""
			if n.seconds {
				unit = "s"
			}
			return d, fmt.Errorf("%s %q: from %g%s to %g%s", n.name, v, n.min, unit, n.max, unit)
		}
		switch p := n.ptr(&d).(type) {
		case *float64:
			*p = f
		case *int:
			*p = int(f)
		}
		if normalise {
			if n.seconds {
				it.Set(n.name, strconv.FormatFloat(f, 'f', -1, 64)+"s")
			} else {
				it.Set(n.name, strconv.FormatFloat(f, 'f', -1, 64))
			}
		}
	}
	if d.NoiseKLow > d.NoiseK {
		return d, fmt.Errorf("noise-k-low (%g) above noise-k (%g): it widens ranges below the threshold", d.NoiseKLow, d.NoiseK)
	}
	return d, nil
}

// MotionDetectorSettings reads /system/motion-detector (the defaults when it is wrong).
func MotionDetectorSettings(s *config.Store) MotionDetector {
	d, err := readMotionDetector(single(s, "/system/motion-detector"), false)
	if err != nil {
		return DefaultMotionDetector
	}
	return d
}

// Signature names the settings: a bar made with others is made again.
func (d MotionDetector) Signature() string {
	b, _ := json.Marshal(d)
	return "v2 " + string(b)
}

// ---------------------------------------------------------------- the stream

type mPacket struct {
	t    float64
	size int
	key  bool
}

type streamStats struct {
	gop, noise, flat float64
	secs             []int           // the seconds with non-key packets, sorted
	r                map[int]float64 // the packet signal: × background
	order            []int           // the seconds of r in the order Python's dict has them
}

func ffprobePath(ffmpeg string) string {
	if strings.Contains(ffmpeg, "/") {
		return filepath.Join(filepath.Dir(ffmpeg), "ffprobe")
	}
	return "ffprobe"
}

// probeDuration: the length of a file (s), 0 when unknown.
func probeDuration(ctx context.Context, ffprobe, path string) float64 {
	out, _ := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path).Output()
	f, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return f
}

// readPackets: time, size and key flag of every video packet, by time.
func readPackets(ctx context.Context, ffprobe, path string) ([]mPacket, error) {
	cmd := exec.CommandContext(ctx, ffprobe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "packet=pts_time,dts_time,size,flags", "-of", "csv=p=0", path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var res []mPacket
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		f := strings.Split(sc.Text(), ",")
		if len(f) < 4 {
			continue
		}
		ts := f[0]
		if ts == "N/A" || ts == "" {
			ts = f[1]
		}
		t, e1 := strconv.ParseFloat(ts, 64)
		size, e2 := strconv.Atoi(f[2])
		if e1 != nil || e2 != nil {
			continue
		}
		res = append(res, mPacket{t, size, strings.Contains(f[3], "K")})
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("ffprobe: %v", err)
	}
	sort.SliceStable(res, func(i, j int) bool {
		if res[i].t != res[j].t {
			return res[i].t < res[j].t
		}
		if res[i].size != res[j].size {
			return res[i].size < res[j].size
		}
		return !res[i].key && res[j].key
	})
	return res, nil
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// trimmedMean: the mean without the largest frames (the "virtual I frames" of H.26x+).
func trimmedMean(xs []float64) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	k := int(math.RoundToEven(float64(len(s)) * (1 - 0.15)))
	if k < 1 {
		k = 1
	}
	sum := 0.0
	for _, x := range s[:k] {
		sum += x
	}
	return sum / float64(k)
}

func (d MotionDetector) analyseStream(pk []mPacket, duration float64) streamStats {
	var keys []float64
	for _, p := range pk {
		if p.key {
			keys = append(keys, p.t)
		}
	}
	var gops []float64
	for i := 1; i < len(keys); i++ {
		gops = append(gops, keys[i]-keys[i-1])
	}
	a := streamStats{gop: duration, r: map[int]float64{}}
	if len(gops) > 0 {
		a.gop = median(gops)
	}

	// the packet signal
	perSec, perSecAll, keySec := map[int][]float64{}, map[int]float64{}, map[int]bool{}
	for _, p := range pk {
		s := int(p.t)
		perSecAll[s] += float64(p.size)
		if p.key {
			keySec[s] = true
		} else {
			perSec[s] = append(perSec[s], float64(p.size))
		}
	}
	for s := range perSec {
		a.secs = append(a.secs, s)
	}
	sort.Ints(a.secs)
	v := map[int]float64{}
	for _, s := range a.secs {
		v[s] = trimmedMean(perSec[s])
	}
	// the background: a moving median, apart for seconds with a key frame and without
	// (P frames just after an I frame are clearly smaller with many encoders)
	var withKey, without []int
	for _, s := range a.secs {
		if keySec[s] {
			withKey = append(withKey, s)
		} else {
			without = append(without, s)
		}
	}
	for _, group := range [][]int{withKey, without} {
		w := d.Window
		if !(len(group) > 0 && float64(len(group)) > float64(len(a.secs))/2) {
			w = max(5, d.Window*len(group)/max(1, len(a.secs)))
		}
		for i, s := range group {
			lo, hi := max(0, i-w), min(len(group), i+w+1)
			win := make([]float64, 0, hi-lo)
			for _, x := range group[lo:hi] {
				win = append(win, v[x])
			}
			if m := median(win); m != 0 {
				a.r[s] = v[s] / m
			} else {
				a.r[s] = 0
			}
			a.order = append(a.order, s)
		}
	}
	vals := make([]float64, 0, len(a.secs))
	for _, s := range a.secs {
		vals = append(vals, math.Abs(a.r[s]-1))
	}
	a.noise = 1.0
	if len(vals) > 0 {
		a.noise = median(vals) * 1.4826
	}

	// CBR: the rate control smooths sums over a longer window. With VBR the noise of a
	// second and of ~10 s are alike (ratio ~1-3); with CBR the window is nearly flat (>8).
	win := max(10, int(math.RoundToEven(2*a.gop)))
	var allSecs []int
	for s := range perSecAll {
		allSecs = append(allSecs, s)
	}
	sort.Ints(allSecs)
	var tot1 []float64
	totwMap := map[int]float64{}
	for _, s := range allSecs {
		tot1 = append(tot1, perSecAll[s])
		totwMap[floorDiv(s, win)] += perSecAll[s]
	}
	tot1 = inner(tot1)
	var wk []int
	for k := range totwMap {
		wk = append(wk, k)
	}
	sort.Ints(wk)
	var totw []float64
	for _, k := range wk {
		totw = append(totw, totwMap[k])
	}
	totw = inner(totw)
	if len(totw) >= 4 {
		a.flat = rmad(tot1) / math.Max(rmad(totw), 1e-4)
	}
	return a
}

func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// inner drops the first and the last (Python's [1:-1]).
func inner(xs []float64) []float64 {
	if len(xs) <= 2 {
		return nil
	}
	return xs[1 : len(xs)-1]
}

func rmad(xs []float64) float64 {
	m := median(xs)
	if len(xs) == 0 || m == 0 {
		return 0
	}
	dev := make([]float64, len(xs))
	for i, x := range xs {
		dev[i] = math.Abs(x - m)
	}
	return median(dev) / m
}

// choose picks the method for a stream.
func (d MotionDetector) choose(a streamStats) (method, why string) {
	reliable := a.noise <= d.NoiseMax && a.flat <= d.CBRFlat
	shortGOP := a.gop <= d.KeyframeMaxGOP
	allowed := map[string]bool{}
	for _, m := range d.Methods {
		allowed[m] = true
	}
	switch d.Mode {
	case "manual":
		return d.Methods[0], "manual"
	case "heuristic":
		switch {
		case allowed[MethodPackets] && reliable:
			return MethodPackets, "the packet sizes follow motion (VBR / smart codec)"
		case allowed[MethodKeyframes] && (shortGOP || !allowed[MethodPixels]):
			return MethodKeyframes, "the packet sizes do not follow motion (CBR / noise)"
		case allowed[MethodPixels]:
			return MethodPixels, "the packet sizes do not follow motion (CBR / noise)"
		}
		return d.Methods[0], "the only method given"
	}
	if reliable {
		return MethodPackets, "the packet sizes follow motion (VBR / smart codec)"
	}
	return MethodKeyframes, "the packet sizes do not follow motion (CBR / noise)"
}

// ---------------------------------------------------------------- ranges

// MotionRange is an event: start, end (s) and its peak (× background for packets, % of
// the picture for keyframes and pixels).
type MotionRange struct {
	Start, End, Peak float64
	Unverified       bool `json:",omitempty"` // packets: beyond max-verify
}

func (d MotionDetector) rangesFrom(times []float64, gap, minDur, step float64, peaks map[float64]float64) []MotionRange {
	var out []MotionRange
	started := false
	var start, last, pk float64
	for _, t := range times {
		if started && t-last > gap {
			out = append(out, MotionRange{Start: start, End: last + step, Peak: pk})
			started = false
		}
		if !started {
			start, pk, started = t, 0, true
		}
		last = t
		if len(peaks) > 0 {
			pk = math.Max(pk, peaks[t])
		}
	}
	if started {
		out = append(out, MotionRange{Start: start, End: last + step, Peak: pk})
	}
	var kept []MotionRange
	for _, x := range out {
		if x.End-x.Start >= minDur {
			kept = append(kept, x)
		}
	}
	// the padding, and ranges that overlap merged
	var merged []MotionRange
	for _, x := range kept {
		a, b := math.Max(0, x.Start-d.Padding), x.End+d.Padding
		if n := len(merged); n > 0 && a <= merged[n-1].End {
			merged[n-1].End = math.Max(merged[n-1].End, b)
			merged[n-1].Peak = math.Max(merged[n-1].Peak, x.Peak)
		} else {
			merged = append(merged, MotionRange{Start: a, End: b, Peak: x.Peak})
		}
	}
	return merged
}

// packetRanges: ranges of the packet signal, a threshold after the noise, hysteresis.
func (d MotionDetector) packetRanges(a streamStats) []MotionRange {
	thr := math.Max(d.RatioMin, 1+d.NoiseK*a.noise)
	low := math.Max(d.RatioMin, 1+d.NoiseKLow*a.noise)
	lastSec := 0
	if len(a.secs) > 0 {
		lastSec = a.secs[len(a.secs)-1]
	}
	in := func(s int) bool { return s > 0 && s < lastSec && float64(s) >= d.Skip }
	seeds := map[int]bool{}
	var weak []int
	for _, s := range a.secs {
		if a.r[s] >= thr && in(s) {
			seeds[s] = true
		}
		if a.r[s] >= low && in(s) {
			weak = append(weak, s)
		}
	}
	var groups [][]int
	var cur []int
	for _, s := range weak {
		if len(cur) > 0 && float64(s-cur[len(cur)-1]) > d.Gap {
			groups = append(groups, cur)
			cur = nil
		}
		cur = append(cur, s)
	}
	if len(cur) > 0 {
		groups = append(groups, cur)
	}
	var cand []float64
	for _, g := range groups {
		hit := false
		for _, s := range g {
			hit = hit || seeds[s]
		}
		if hit {
			for _, s := range g {
				cand = append(cand, float64(s))
			}
		}
	}
	sort.Float64s(cand)
	peaks := map[float64]float64{}
	for s, x := range a.r {
		peaks[float64(s)] = x
	}
	return d.rangesFrom(cand, d.Gap, d.MinDuration, 1, peaks)
}

type pixSample struct{ t, pct float64 }

func (d MotionDetector) pixelRanges(samples []pixSample, step float64) []MotionRange {
	var times []float64
	peaks := map[float64]float64{}
	for _, s := range samples {
		if s.pct >= d.PixelMinPercent && s.t >= d.Skip {
			times = append(times, s.t)
		}
		peaks[s.t] = s.pct
	}
	minDur := d.MinDuration
	if step > 1 {
		minDur = 0
	}
	return d.rangesFrom(times, math.Max(d.Gap, step*1.5), minDur, step, peaks)
}

// ---------------------------------------------------------------- the picture

func (d MotionDetector) pixelFilter(fps float64) string {
	var f []string
	if fps > 0 {
		f = append(f, "fps="+strconv.FormatFloat(fps, 'f', -1, 64))
	}
	f = append(f, fmt.Sprintf("scale=%d:-2:flags=area", d.PixelWidth), "format=gray",
		fmt.Sprintf("crop=iw:ih-%d:0:%d", d.OSDHeight, d.OSDHeight), "gblur=sigma=1.5",
		"tblend=all_mode=difference", fmt.Sprintf(`lut=y='gt(val\,%d)*255'`, d.PixelDiff),
		"signalstats", "metadata=print:key=lavfi.signalstats.YAVG:file=-")
	return strings.Join(f, ",")
}

// runPixels: the share of changed pixels (%) per frame looked at. ss < 0: from the start.
func (d MotionDetector) runPixels(ctx context.Context, ffmpeg, path string, keyframesOnly bool, ss, dur float64, hw bool) ([]pixSample, error) {
	args := []string{"-hide_banner", "-nostats", "-nostdin", "-v", "error"}
	if hw {
		args = append(args, "-hwaccel", "auto")
	}
	args = append(args, "-skip_loop_filter", "all")
	if keyframesOnly {
		args = append(args, "-skip_frame", "nokey")
	}
	if ss >= 0 {
		args = append(args, "-ss", fmt.Sprintf("%.3f", ss))
	}
	if dur > 0 {
		args = append(args, "-t", fmt.Sprintf("%.3f", dur))
	}
	fps := d.PixelFPS
	if keyframesOnly {
		fps = 0
	}
	args = append(args, "-i", path, "-an", "-sn", "-dn", "-vf", d.pixelFilter(fps), "-f", "null", "-")
	out, err := exec.CommandContext(ctx, ffmpeg, args...).Output()
	if err != nil {
		if hw && ctx.Err() == nil { // no hardware decoding: in software
			return d.runPixels(ctx, ffmpeg, path, keyframesOnly, ss, dur, false)
		}
		return nil, fmt.Errorf("ffmpeg: %v", err)
	}
	var res []pixSample
	cur, have := 0.0, false
	for _, line := range strings.Split(string(out), "\n") {
		if i := strings.Index(line, "pts_time:"); i >= 0 {
			f := strings.Fields(line[i+len("pts_time:"):])
			have = false
			if len(f) > 0 {
				if t, err := strconv.ParseFloat(f[0], 64); err == nil {
					cur, have = t, true
				}
			}
		} else if strings.Contains(line, "YAVG=") && have {
			_, v, _ := strings.Cut(line, "=")
			y, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				continue
			}
			pct := y * 100 / 255
			if pct > d.GlobalPercent { // the whole picture at once: IR, exposure
				pct = 0
			}
			t := cur
			if ss >= 0 {
				t += ss
			}
			res = append(res, pixSample{t, pct})
		}
	}
	return res, nil
}

// verify decodes 2 s around the peak of every range: no motion in the picture, it goes.
func (d MotionDetector) verify(ctx context.Context, ffmpeg, path string, ranges []MotionRange, a streamStats) ([]MotionRange, int) {
	var out []MotionRange
	dropped := 0
	for i, x := range ranges {
		if i >= d.MaxVerify {
			x.Unverified = true
			out = append(out, x)
			continue
		}
		peak, best, found := x.Start, 0.0, false
		for _, s := range a.order {
			if f := float64(s); x.Start <= f && f <= x.End && (!found || a.r[s] > best) {
				peak, best, found = f, a.r[s], true
			}
		}
		smp, err := d.runPixels(ctx, ffmpeg, path, false, math.Max(0, peak-0.5), 2, true)
		if err != nil && ctx.Err() != nil {
			return out, dropped
		}
		mx := 0.0
		for _, s := range smp {
			mx = math.Max(mx, s.pct)
		}
		if mx >= d.PixelMinPercent {
			out = append(out, x)
		} else {
			dropped++
		}
	}
	return out, dropped
}

// Detection is what the detector found in a file.
type Detection struct {
	Method, Why string
	Duration    float64
	Ranges      []MotionRange
	Dropped     int // packets: ranges without motion in the picture
	GOP, Noise  float64
	Flat        float64
}

// Detect looks for motion in a file.
func (d MotionDetector) Detect(ctx context.Context, ffmpeg, path string) (Detection, error) {
	ffprobe := ffprobePath(ffmpeg)
	pk, err := readPackets(ctx, ffprobe, path)
	if err != nil {
		return Detection{}, err
	}
	if len(pk) == 0 {
		return Detection{}, errors.New("no video packets")
	}
	dur := probeDuration(ctx, ffprobe, path)
	if dur <= 0 {
		dur = pk[len(pk)-1].t - pk[0].t
	}
	a := d.analyseStream(pk, dur)
	det := Detection{Duration: dur, GOP: a.gop, Noise: a.noise, Flat: a.flat}
	det.Method, det.Why = d.choose(a)
	switch det.Method {
	case MethodPackets:
		det.Ranges, det.Dropped = d.verify(ctx, ffmpeg, path, d.packetRanges(a), a)
	case MethodKeyframes:
		smp, err := d.runPixels(ctx, ffmpeg, path, true, -1, 0, true)
		if err != nil {
			return det, err
		}
		det.Ranges = d.pixelRanges(smp, a.gop)
	default:
		smp, err := d.runPixels(ctx, ffmpeg, path, false, -1, 0, true)
		if err != nil {
			return det, err
		}
		det.Ranges = d.pixelRanges(smp, 1/d.PixelFPS)
	}
	if err := ctx.Err(); err != nil {
		return det, err
	}
	return det, nil
}

// motionTimeout: a file analysed longer than this is given up (pixels of a long file).
var motionTimeout = 30 * time.Minute
