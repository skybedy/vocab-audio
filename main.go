package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type config struct {
	SpanishInput string
	CzechInput   string
	Output       string
	ItemID       string
	InputDir     string
	OutputDir    string

	Noise         string
	MinSilence    float64
	ExpectedItems int
	ShortPause    float64
	LongPause     float64
}

type segment struct {
	Start float64
	End   float64
}

type silence struct {
	Start float64
	End   float64
}

const (
	// Use one simple audio shape for every intermediate file so concat stays predictable.
	wavSampleRate     = "44100"
	minSegmentSeconds = 0.08
	edgePadding       = 0.03
	refineMinSilence  = 0.08
	silenceMergeSlop  = 0.02
)

func main() {
	cfg := parseFlags()

	var err error
	cfg, err = resolveItemConfig(cfg)
	if err != nil {
		exitErr(err)
	}
	if err := validateConfig(cfg); err != nil {
		exitErr(err)
	}
	if err := requireBinary("ffmpeg"); err != nil {
		exitErr(err)
	}
	if err := requireBinary("ffprobe"); err != nil {
		exitErr(err)
	}

	if err := run(cfg); err != nil {
		exitErr(err)
	}
}

func parseFlags() config {
	cfg := config{}
	flag.StringVar(&cfg.ItemID, "id", "", "Input item id, for example 01; resolves files from -input-dir and writes to -output-dir")
	flag.StringVar(&cfg.InputDir, "input-dir", "input", "Directory with *_es.txt, *_es.mp3, and *_cz.mp3 files used by -id")
	flag.StringVar(&cfg.OutputDir, "output-dir", "output", "Directory for generated MP3 files used by -id")
	flag.StringVar(&cfg.SpanishInput, "es", "spanish.mp3", "Spanish MP3 input file")
	flag.StringVar(&cfg.CzechInput, "cs", "czech.mp3", "Czech MP3 input file")
	flag.StringVar(&cfg.Output, "out", "output.mp3", "Output MP3 file")
	flag.StringVar(&cfg.Noise, "noise", "-35dB", "Silence threshold for ffmpeg silencedetect, for example -30dB or -40dB")
	flag.Float64Var(&cfg.MinSilence, "min-silence", 0.45, "Minimum silence duration in seconds")
	flag.IntVar(&cfg.ExpectedItems, "items", 0, "Expected number of Spanish/Czech items; 0 disables count refinement")
	flag.Float64Var(&cfg.ShortPause, "pause-short", 0.6, "Pause between Spanish and Czech segment in seconds")
	flag.Float64Var(&cfg.LongPause, "pause-long", 1.2, "Pause between Czech segment and next Spanish segment in seconds")
	flag.Parse()
	return cfg
}

func validateConfig(cfg config) error {
	if cfg.SpanishInput == "" {
		return errors.New("missing -es input file")
	}
	if cfg.CzechInput == "" {
		return errors.New("missing -cs input file")
	}
	if cfg.Output == "" {
		return errors.New("missing -out output file")
	}
	for name, value := range map[string]float64{
		"-min-silence": cfg.MinSilence,
		"-pause-short": cfg.ShortPause,
		"-pause-long":  cfg.LongPause,
	} {
		if value < 0 {
			return fmt.Errorf("%s must not be negative", name)
		}
	}
	if cfg.ExpectedItems < 0 {
		return errors.New("-items must not be negative")
	}
	if _, err := os.Stat(cfg.SpanishInput); err != nil {
		return fmt.Errorf("cannot read Spanish input %q: %w", cfg.SpanishInput, err)
	}
	if _, err := os.Stat(cfg.CzechInput); err != nil {
		return fmt.Errorf("cannot read Czech input %q: %w", cfg.CzechInput, err)
	}
	return nil
}

func resolveItemConfig(cfg config) (config, error) {
	if cfg.ItemID == "" {
		return cfg, nil
	}
	if cfg.InputDir == "" {
		return cfg, errors.New("missing -input-dir")
	}
	if cfg.OutputDir == "" {
		return cfg, errors.New("missing -output-dir")
	}

	stem, items, err := findInputStem(cfg.InputDir, cfg.ItemID)
	if err != nil {
		return cfg, err
	}

	cfg.SpanishInput = filepath.Join(cfg.InputDir, cfg.ItemID+"_es.mp3")
	cfg.CzechInput = filepath.Join(cfg.InputDir, cfg.ItemID+"_cz.mp3")
	cfg.Output = filepath.Join(cfg.OutputDir, outputStem(stem)+".mp3")
	cfg.ExpectedItems = items

	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		return cfg, fmt.Errorf("create output directory %q: %w", cfg.OutputDir, err)
	}
	return cfg, nil
}

func findInputStem(inputDir, itemID string) (string, int, error) {
	pattern := filepath.Join(inputDir, itemID+"-*_es.txt")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", 0, fmt.Errorf("find input text files: %w", err)
	}
	if len(matches) == 0 {
		return "", 0, fmt.Errorf("no Spanish text file found for id %q using %s", itemID, pattern)
	}
	if len(matches) > 1 {
		return "", 0, fmt.Errorf("more than one Spanish text file found for id %q: %s", itemID, strings.Join(matches, ", "))
	}

	name := filepath.Base(matches[0])
	stem := strings.TrimSuffix(name, "_es.txt")
	items, err := countNonEmptyLines(matches[0])
	if err != nil {
		return "", 0, err
	}
	if items == 0 {
		return "", 0, fmt.Errorf("Spanish text file %q has no items", matches[0])
	}
	return stem, items, nil
}

func outputStem(inputStem string) string {
	number, name, ok := strings.Cut(inputStem, "-")
	if !ok {
		return inputStem
	}
	return number + "_" + name
}

func countNonEmptyLines(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}
	defer file.Close()

	count := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "" {
			count++
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}
	return count, nil
}

func requireBinary(name string) error {
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("%s was not found in PATH", name)
	}
	return nil
}

func run(cfg config) error {
	workDir, err := os.MkdirTemp("", "vocab-audio-*")
	if err != nil {
		return fmt.Errorf("create temporary directory: %w", err)
	}
	defer os.RemoveAll(workDir)

	spanishWAV := filepath.Join(workDir, "spanish.wav")
	czechWAV := filepath.Join(workDir, "czech.wav")

	fmt.Fprintf(os.Stderr, "Converting inputs to WAV...\n")
	if err := convertToWAV(cfg.SpanishInput, spanishWAV); err != nil {
		return err
	}
	if err := convertToWAV(cfg.CzechInput, czechWAV); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Detecting Spanish segments...\n")
	spanishSegments, err := detectSpeechSegments(spanishWAV, cfg.Noise, cfg.MinSilence, cfg.ExpectedItems)
	if err != nil {
		return fmt.Errorf("detect Spanish segments: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Detecting Czech segments...\n")
	czechSegments, err := detectSpeechSegments(czechWAV, cfg.Noise, cfg.MinSilence, cfg.ExpectedItems)
	if err != nil {
		return fmt.Errorf("detect Czech segments: %w", err)
	}

	if len(spanishSegments) == 0 {
		return errors.New("no Spanish speech segments found")
	}
	if len(czechSegments) == 0 {
		return errors.New("no Czech speech segments found")
	}

	if len(spanishSegments) != len(czechSegments) {
		return fmt.Errorf(
			"found %d Spanish segments and %d Czech segments; counts differ, so output would be misaligned. Use -items when you know the expected count",
			len(spanishSegments),
			len(czechSegments),
		)
	}
	pairCount := len(spanishSegments)

	fmt.Fprintf(os.Stderr, "Cutting and assembling %d pairs...\n", pairCount)
	shortPause, longPause, err := createPauseFiles(workDir, cfg.ShortPause, cfg.LongPause)
	if err != nil {
		return err
	}

	files, err := cutAndOrderSegments(workDir, spanishWAV, czechWAV, spanishSegments, czechSegments, pairCount, shortPause, longPause)
	if err != nil {
		return err
	}

	listPath := filepath.Join(workDir, "concat.txt")
	if err := writeConcatList(listPath, files); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Encoding %s...\n", cfg.Output)
	if err := concatToMP3(listPath, cfg.Output); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Done: %s\n", cfg.Output)
	return nil
}

func convertToWAV(input, output string) error {
	return runCommand("ffmpeg",
		"-hide_banner", "-y",
		"-i", input,
		"-vn",
		"-ac", "1",
		"-ar", wavSampleRate,
		"-c:a", "pcm_s16le",
		output,
	)
}

func detectSpeechSegments(wavPath, noise string, minSilence float64, expectedCount int) ([]segment, error) {
	duration, err := probeDuration(wavPath)
	if err != nil {
		return nil, err
	}

	silences, err := detectSilences(wavPath, noise, minSilence, duration)
	if err != nil {
		return nil, err
	}

	if expectedCount > 0 {
		silences, err = refineSilencesToExpectedCount(wavPath, noise, silences, duration, expectedCount)
		if err != nil {
			return nil, err
		}
	}

	segments := speechFromSilences(silences, duration)
	if expectedCount > 0 && len(segments) != expectedCount {
		return nil, fmt.Errorf("found %d segments, expected %d", len(segments), expectedCount)
	}
	return segments, nil
}

func detectSilences(wavPath, noise string, minSilence, duration float64) ([]silence, error) {
	output, err := commandOutput("ffmpeg",
		"-hide_banner",
		"-i", wavPath,
		"-af", fmt.Sprintf("silencedetect=noise=%s:d=%.3f", noise, minSilence),
		"-f", "null",
		"-",
	)
	if err != nil {
		return nil, err
	}
	return parseSilences(output, duration), nil
}

func refineSilencesToExpectedCount(wavPath, noise string, silences []silence, duration float64, expectedCount int) ([]silence, error) {
	missing := expectedCount - len(speechFromSilences(silences, duration))
	if missing <= 0 {
		return silences, nil
	}

	candidates, err := detectSilences(wavPath, noise, refineMinSilence, duration)
	if err != nil {
		return nil, err
	}
	extras := extraSilenceCandidates(candidates, silences)
	if len(extras) < missing {
		return nil, fmt.Errorf("found %d segments, expected %d, and only %d extra short-pause candidates were available",
			len(speechFromSilences(silences, duration)), expectedCount, len(extras))
	}

	sort.SliceStable(extras, func(i, j int) bool {
		return extras[i].End-extras[i].Start > extras[j].End-extras[j].Start
	})

	current := len(speechFromSilences(silences, duration))
	for _, extra := range extras {
		if current >= expectedCount {
			break
		}

		next := append(append([]silence{}, silences...), extra)
		sort.Slice(next, func(i, j int) bool {
			return next[i].Start < next[j].Start
		})

		nextCount := len(speechFromSilences(next, duration))
		if nextCount <= current {
			continue
		}

		silences = next
		current = nextCount
	}
	return silences, nil
}

func extraSilenceCandidates(candidates, existing []silence) []silence {
	var extras []silence
	for _, candidate := range candidates {
		if overlapsAnySilence(candidate, existing) {
			continue
		}
		extras = append(extras, candidate)
	}
	return extras
}

func overlapsAnySilence(candidate silence, silences []silence) bool {
	for _, s := range silences {
		if candidate.Start < s.End+silenceMergeSlop && candidate.End > s.Start-silenceMergeSlop {
			return true
		}
	}
	return false
}

func probeDuration(path string) (float64, error) {
	out, err := commandOutput("ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	)
	if err != nil {
		return 0, err
	}
	value := strings.TrimSpace(out)
	duration, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("parse duration %q: %w", value, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("invalid duration %.3f for %s", duration, path)
	}
	return duration, nil
}

func parseSilences(ffmpegOutput string, duration float64) []silence {
	startRe := regexp.MustCompile(`silence_start:\s*([0-9.]+)`)
	endRe := regexp.MustCompile(`silence_end:\s*([0-9.]+)`)

	var silences []silence
	var openStart *float64

	scanner := bufio.NewScanner(strings.NewReader(ffmpegOutput))
	for scanner.Scan() {
		line := scanner.Text()
		if match := startRe.FindStringSubmatch(line); len(match) == 2 {
			value, err := strconv.ParseFloat(match[1], 64)
			if err == nil {
				openStart = &value
			}
			continue
		}
		if match := endRe.FindStringSubmatch(line); len(match) == 2 {
			value, err := strconv.ParseFloat(match[1], 64)
			if err == nil && openStart != nil {
				silences = append(silences, silence{Start: *openStart, End: value})
				openStart = nil
			}
		}
	}

	// ffmpeg may report silence_start at EOF without a matching silence_end.
	if openStart != nil {
		silences = append(silences, silence{Start: *openStart, End: duration})
	}

	sort.Slice(silences, func(i, j int) bool {
		return silences[i].Start < silences[j].Start
	})
	return silences
}

// speechFromSilences turns silence intervals into speech intervals and keeps a
// tiny edge pad so word starts and endings are not clipped too aggressively.
func speechFromSilences(silences []silence, duration float64) []segment {
	var segments []segment
	cursor := 0.0

	for _, s := range silences {
		start := clamp(cursor-edgePadding, 0, duration)
		end := clamp(s.Start+edgePadding, 0, duration)
		if end-start >= minSegmentSeconds {
			segments = append(segments, segment{Start: start, End: end})
		}
		cursor = clamp(s.End, 0, duration)
	}

	start := clamp(cursor-edgePadding, 0, duration)
	end := duration
	if end-start >= minSegmentSeconds {
		segments = append(segments, segment{Start: start, End: end})
	}

	return segments
}

func createPauseFiles(workDir string, shortPause, longPause float64) (string, string, error) {
	shortPath := filepath.Join(workDir, "pause_short.wav")
	longPath := filepath.Join(workDir, "pause_long.wav")
	if err := createSilenceWAV(shortPath, shortPause); err != nil {
		return "", "", err
	}
	if err := createSilenceWAV(longPath, longPause); err != nil {
		return "", "", err
	}
	return shortPath, longPath, nil
}

func createSilenceWAV(output string, duration float64) error {
	if duration == 0 {
		duration = 0.001
	}
	return runCommand("ffmpeg",
		"-hide_banner", "-y",
		"-f", "lavfi",
		"-i", fmt.Sprintf("anullsrc=r=%s:cl=mono", wavSampleRate),
		"-t", fmt.Sprintf("%.3f", duration),
		"-c:a", "pcm_s16le",
		output,
	)
}

func cutAndOrderSegments(workDir, spanishWAV, czechWAV string, spanishSegments, czechSegments []segment, pairCount int, shortPause, longPause string) ([]string, error) {
	var files []string

	for i := 0; i < pairCount; i++ {
		esPath := filepath.Join(workDir, fmt.Sprintf("segment_%04d_es.wav", i+1))
		csPath := filepath.Join(workDir, fmt.Sprintf("segment_%04d_cs.wav", i+1))

		if err := cutSegment(spanishWAV, esPath, spanishSegments[i]); err != nil {
			return nil, fmt.Errorf("cut Spanish segment %d: %w", i+1, err)
		}
		if err := cutSegment(czechWAV, csPath, czechSegments[i]); err != nil {
			return nil, fmt.Errorf("cut Czech segment %d: %w", i+1, err)
		}

		files = append(files, esPath, shortPause, csPath)
		if i != pairCount-1 {
			files = append(files, longPause)
		}
	}

	return files, nil
}

func cutSegment(input, output string, s segment) error {
	duration := math.Max(s.End-s.Start, minSegmentSeconds)
	return runCommand("ffmpeg",
		"-hide_banner", "-y",
		"-ss", fmt.Sprintf("%.3f", s.Start),
		"-t", fmt.Sprintf("%.3f", duration),
		"-i", input,
		"-vn",
		"-ac", "1",
		"-ar", wavSampleRate,
		"-c:a", "pcm_s16le",
		output,
	)
}

func writeConcatList(path string, files []string) error {
	var b strings.Builder
	for _, file := range files {
		b.WriteString("file '")
		b.WriteString(strings.ReplaceAll(file, "'", "'\\''"))
		b.WriteString("'\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("write concat list: %w", err)
	}
	return nil
}

func concatToMP3(listPath, output string) error {
	return runCommand("ffmpeg",
		"-hide_banner", "-y",
		"-f", "concat",
		"-safe", "0",
		"-i", listPath,
		"-vn",
		"-c:a", "libmp3lame",
		"-q:a", "2",
		output,
	)
}

func runCommand(name string, args ...string) error {
	_, err := commandOutput(name, args...)
	return err
}

func commandOutput(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s failed: %w\n%s", name, err, strings.TrimSpace(out.String()))
	}
	return out.String(), nil
}

func clamp(value, minValue, maxValue float64) float64 {
	return math.Max(minValue, math.Min(maxValue, value))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func exitErr(err error) {
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	os.Exit(1)
}
