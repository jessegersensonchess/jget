package main

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"etl/internal/metrics"
	"etl/internal/metrics/datadog"
)

// logRecord is emitted as JSONL to stdout for each URL attempt.
//
// This output is intended for machine parsing. Additive changes are safe;
// renames/removals are breaking changes for downstream log consumers.
type logRecord struct {
	Timestamp    string            `json:"ts"`
	URL          string            `json:"url"`
	Attempt      int               `json:"attempt"`
	StatusCode   int               `json:"http_code"`
	DurationMs   int64             `json:"duration_ms"`
	RequestMs    int64             `json:"request_ms"`
	ResponseMs   int64             `json:"response_ms"`
	DownloadSz   int64             `json:"size_bytes"`
	File         string            `json:"file,omitempty"`
	Error        string            `json:"error,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	RetryAfterMs int64             `json:"retry_after_ms,omitempty"`
}

// backendCloser is the minimal interface used by this command to manage a metrics backend.
type backendCloser interface {
	metrics.Backend
	Close() error
}

// deps are external seams for testability.
//
// When to use:
//   - Unit tests: inject fake backend factory and capture stdout/stderr.
//   - Alternate runtimes: swap metrics backend or output sinks.
//
// Errors:
//   - BackendFactory should return a non-nil error for fatal initialization failures.
type deps struct {
	Stdout io.Writer
	Stderr io.Writer

	BackendFactory func(ctx context.Context, jobName string, tags []string, flushEvery time.Duration) (backendCloser, error)
	Now            func() time.Time
	Sleep          func(d time.Duration)
}

// runConfig holds the parsed flags and derived values for a run.
type runConfig struct {
	URLFile         string
	Workers         int
	Timeout         time.Duration
	OutDir          string
	JobName         string
	MaxAttempts     int
	BaseBackoff     time.Duration
	MaxBackoff      time.Duration
	JitterMax       time.Duration
	SleepBefore     time.Duration
	LogHeadersOn429 bool
	DDTagsCSV       string
	FlushEvery      time.Duration

	MaxConnsPerHost int
}

// main is intentionally small: it wires real dependencies and exits with a code.
func main() {
	code := run(context.Background(), os.Args[1:], deps{
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		BackendFactory: func(ctx context.Context, jobName string, tags []string, flushEvery time.Duration) (backendCloser, error) {
			return datadog.NewBackend(ctx, datadog.Options{
				JobName:    jobName,
				Tags:       tags,
				FlushEvery: flushEvery,
			})
		},
		Now:   time.Now,
		Sleep: time.Sleep,
	})
	os.Exit(code)
}

// run executes the crawler command and returns an exit code.
//
// Exit codes:
//   - 0: success.
//   - 1: at least one URL exhausted retries (non-404).
//   - 2: configuration/initialization error.
func run(ctx context.Context, args []string, d deps) int {
	if d.Stdout == nil {
		d.Stdout = io.Discard
	}
	if d.Stderr == nil {
		d.Stderr = io.Discard
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Sleep == nil {
		d.Sleep = time.Sleep
	}
	if d.BackendFactory == nil {
		fmt.Fprintln(d.Stderr, "internal error: BackendFactory is nil")
		return 2
	}

	cfg, err := parseFlags(args)
	if err != nil {
		fmt.Fprintln(d.Stderr, err.Error())
		return 2
	}

	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		fmt.Fprintf(d.Stderr, "failed to create output directory: %v\n", err)
		return 2
	}

	urls, err := readURLs(cfg.URLFile)
	if err != nil {
		fmt.Fprintf(d.Stderr, "error reading urls: %v\n", err)
		return 2
	}
	if len(urls) == 0 {
		fmt.Fprintf(d.Stderr, "no URLs found in %s\n", cfg.URLFile)
		return 2
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	tags := append(datadog.ParseTagsCSV(cfg.DDTagsCSV), "tool:crawl_urls")
	backend, err := d.BackendFactory(ctx, cfg.JobName, tags, cfg.FlushEvery)
	if err != nil {
		fmt.Fprintf(d.Stderr, "datadog backend init failed: %v\n", err)
		return 2
	}
	metrics.SetBackend(backend)
	defer func() {
		_ = metrics.Flush()
		_ = backend.Close()
	}()

	client := newHTTPClient(cfg.Timeout, cfg.MaxConnsPerHost)

	jobs := make(chan string)
	logCh := make(chan logRecord, 512)

	// Fail fast on the first URL that exhausts retries.
	var fatalMu sync.Mutex
	fatal := false
	setFatal := func() {
		fatalMu.Lock()
		fatal = true
		fatalMu.Unlock()
	}
	isFatal := func() bool {
		fatalMu.Lock()
		defer fatalMu.Unlock()
		return fatal
	}

	// Logger goroutine.
	var logWG sync.WaitGroup
	logWG.Add(1)
	go func() {
		defer logWG.Done()
		writeJSONLines(d.Stdout, logCh)
	}()

	// Workers.
	var wg sync.WaitGroup
	wg.Add(cfg.Workers)
	for i := 0; i < cfg.Workers; i++ {
		workerID := i
		rng := rand.New(rand.NewSource(d.Now().UnixNano() + int64(workerID)*9973))

		go func() {
			defer wg.Done()

			runWorker(ctx, workerID, rng, client, jobs, logCh, workerConfig{
				jobName:       cfg.JobName,
				outDir:        cfg.OutDir,
				maxAttempts:   cfg.MaxAttempts,
				baseBackoff:   cfg.BaseBackoff,
				maxBackoff:    cfg.MaxBackoff,
				sleepBefore:   cfg.SleepBefore,
				jitterMax:     cfg.JitterMax,
				logHeaders429: cfg.LogHeadersOn429,
			}, setFatal, cancel, d.Sleep)
		}()
	}

	// Producer.
	go func() {
		defer close(jobs)
		for _, u := range urls {
			select {
			case <-ctx.Done():
				return
			case jobs <- u:
			}
		}
	}()

	wg.Wait()
	close(logCh)
	logWG.Wait()

	_ = metrics.Flush()

	if isFatal() {
		return 1
	}
	return 0
}

// parseFlags parses command arguments into a validated runConfig.
//
// Errors:
//   - Returns an error for invalid/missing required flags.
//   - Does not exit the process (caller decides exit code).
func parseFlags(args []string) (runConfig, error) {
	fs := flag.NewFlagSet("jget", flag.ContinueOnError)

	// Capture help/usage text instead of writing to stdout.
	var usageBuf strings.Builder
	fs.SetOutput(&usageBuf)

	// Ensure -h / -help prints the typical defaults list.
	fs.Usage = func() {
		fmt.Fprintf(&usageBuf, "Usage of %s:\n", fs.Name())
		fs.PrintDefaults()
	}

	var cfg runConfig
	fs.StringVar(&cfg.URLFile, "i", "", "Path to file containing URLs (one per line)")
	fs.IntVar(&cfg.Workers, "n", 4, "Number of concurrent workers")
	fs.DurationVar(&cfg.Timeout, "t", 60*time.Second, "HTTP timeout per request (e.g. 60s)")
	fs.StringVar(&cfg.OutDir, "o", "out", "Directory to save downloaded bodies")
	fs.StringVar(&cfg.JobName, "name", "default_crawl", "Logical job name used in metrics tags")
	fs.IntVar(&cfg.MaxAttempts, "max_attempts", 30, "Max attempts per URL (including first attempt)")
	fs.DurationVar(&cfg.BaseBackoff, "base_backoff", 2*time.Second, "Base backoff for retries (non-429)")
	fs.DurationVar(&cfg.MaxBackoff, "max_backoff", 60*time.Second, "Max backoff for retries (non-429)")
	fs.DurationVar(&cfg.JitterMax, "jitter_max", 350*time.Millisecond, "Max jitter added to sleeps")
	fs.DurationVar(&cfg.SleepBefore, "sleep_before", 200*time.Millisecond, "Base sleep before each request")
	fs.BoolVar(&cfg.LogHeadersOn429, "log_headers_on_429", true, "Include response headers in logs for HTTP 429")
	fs.StringVar(&cfg.DDTagsCSV, "dd_tags", "", "Extra Datadog tags CSV (e.g. env:prod,service:etl)")
	fs.DurationVar(&cfg.FlushEvery, "metrics_flush", 1*time.Minute, "Datadog flush interval (default 1m)")

	fs.IntVar(&cfg.MaxConnsPerHost, "max_conns_per_host", 32, "Max HTTP connections per host (0 means unlimited)")

	if err := fs.Parse(args); err != nil {
		// When -h / -help is passed, flag.Parse returns flag.ErrHelp.
		// Return the captured usage text so caller prints it.
		if errors.Is(err, flag.ErrHelp) {
			return runConfig{}, errors.New(usageBuf.String())
		}
		// For other parse errors, return the error plus usage (nice UX).
		return runConfig{}, fmt.Errorf("%v\n\n%s", err, usageBuf.String())
	}

	if cfg.URLFile == "" {
		return runConfig{}, errors.New("missing required -i <url_file>")
	}
	if cfg.Workers <= 0 {
		return runConfig{}, errors.New("-n must be > 0")
	}
	if cfg.MaxAttempts <= 0 {
		return runConfig{}, errors.New("-max_attempts must be > 0")
	}
	if cfg.MaxConnsPerHost < 0 {
		return runConfig{}, errors.New("-max_conns_per_host must be >= 0")
	}

	return cfg, nil
}

type workerConfig struct {
	jobName       string
	outDir        string
	maxAttempts   int
	baseBackoff   time.Duration
	maxBackoff    time.Duration
	sleepBefore   time.Duration
	jitterMax     time.Duration
	logHeaders429 bool
}

func runWorker(
	ctx context.Context,
	workerID int,
	rng *rand.Rand,
	client *http.Client,
	jobs <-chan string,
	logCh chan<- logRecord,
	cfg workerConfig,
	setFatal func(),
	cancel context.CancelFunc,
	sleep func(d time.Duration),
) {
	sleeper := newSleeper(rng, cfg.sleepBefore, cfg.jitterMax, sleep)

	for {
		select {
		case <-ctx.Done():
			return
		case rawURL, ok := <-jobs:
			if !ok {
				return
			}

			okLoad := processURL(ctx, client, rawURL, cfg, sleeper, logCh)
			if okLoad {
				continue
			}

			setFatal()
			cancel()
			return
		}
	}
}

func processURL(
	ctx context.Context,
	client *http.Client,
	rawURL string,
	cfg workerConfig,
	sleeper *sleeper,
	logCh chan<- logRecord,
) bool {
	outputPath := filepath.Join(cfg.outDir, hashString(rawURL))

	for attempt := 1; attempt <= cfg.maxAttempts; attempt++ {
		sleeper.Sleep()

		rec := doAttempt(ctx, client, rawURL, attempt, outputPath, cfg.logHeaders429)

		// Record metrics per attempt.
		var attemptErr error
		if rec.Error != "" {
			attemptErr = fmt.Errorf("%s", rec.Error)
		}
		metrics.RecordHTTP(
			cfg.jobName,
			rec.StatusCode,
			attemptErr,
			time.Duration(rec.RequestMs)*time.Millisecond,
			time.Duration(rec.ResponseMs)*time.Millisecond,
			rec.DownloadSz,
		)

		logCh <- rec

		if rec.StatusCode >= 200 && rec.StatusCode < 300 {
			return true
		}
		if rec.StatusCode == http.StatusNotFound {
			// Keep existing semantics: missing resource is not a pipeline failure.
			return true
		}
		if attempt == cfg.maxAttempts {
			return false
		}

		wait := nextRetryDelay(rec, attempt, cfg.baseBackoff, cfg.maxBackoff)
		if !sleepContext(ctx, wait) {
			return false
		}
	}

	return false
}

func doAttempt(
	ctx context.Context,
	client *http.Client,
	rawURL string,
	attempt int,
	outputPath string,
	logHeaders429 bool,
) logRecord {
	start := time.Now()

	rec := logRecord{
		Timestamp:  time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		URL:        rawURL,
		Attempt:    attempt,
		StatusCode: 0,
		RequestMs:  -1,
		ResponseMs: -1,
		DownloadSz: -1,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		rec.DurationMs = time.Since(start).Milliseconds()
		rec.Error = err.Error()
		return rec
	}

	resp, err := client.Do(req)
	if err != nil {
		rec.DurationMs = time.Since(start).Milliseconds()
		rec.Error = err.Error()
		return rec
	}
	rec.RequestMs = time.Since(start).Milliseconds()
	defer resp.Body.Close()

	rec.StatusCode = resp.StatusCode

	// For non-2xx, discard the body so connections can be reused.
	// For 2xx, stream directly to the output file to avoid buffering large bodies.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		n, werr := writeBodyToFile(outputPath, resp.Body)
		rec.DownloadSz = n
		if werr != nil {
			rec.Error = werr.Error()
		} else {
			rec.File = outputPath
		}
	} else {
		n, derr := io.Copy(io.Discard, resp.Body)
		rec.DownloadSz = n
		if derr != nil {
			rec.Error = derr.Error()
		}
	}

	rec.ResponseMs = time.Since(start).Milliseconds()
	rec.DurationMs = rec.ResponseMs

	if resp.StatusCode == http.StatusTooManyRequests && logHeaders429 {
		rec.Headers = flattenHeaders(resp.Header, 64)
		rec.RetryAfterMs = parseRetryAfter(resp.Header).Milliseconds()
	}

	return rec
}

// writeBodyToFile writes r to outputPath atomically.
//
// Behavior:
//   - Writes to a temp file in the same directory.
//   - Renames into place on success.
//   - On failure, attempts to remove the temp file.
//
// Returns the number of bytes written.
func writeBodyToFile(outputPath string, r io.Reader) (int64, error) {
	dir := filepath.Dir(outputPath)
	tmp, err := os.CreateTemp(dir, ".jget-*")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()

	n, copyErr := io.Copy(tmp, r)
	closeErr := tmp.Close()

	if copyErr != nil {
		_ = os.Remove(tmpName)
		return n, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpName)
		return n, closeErr
	}
	if err := os.Rename(tmpName, outputPath); err != nil {
		_ = os.Remove(tmpName)
		return n, err
	}
	return n, nil
}

func nextRetryDelay(rec logRecord, attempt int, base, max time.Duration) time.Duration {
	if rec.StatusCode == http.StatusTooManyRequests && rec.RetryAfterMs > 0 {
		return time.Duration(rec.RetryAfterMs) * time.Millisecond
	}

	// Exponential: base * 2^(attempt-1), clamped.
	d := base << uint(attempt-1)
	if d > max {
		d = max
	}

	// Network error case (status=0): enforce a minimum to reduce tight loops.
	if rec.StatusCode == 0 && d < 10*time.Second {
		d = 10 * time.Second
	}

	return d
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}

	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func parseRetryAfter(h http.Header) time.Duration {
	ra := strings.TrimSpace(h.Get("Retry-After"))
	if ra == "" {
		return 0
	}

	// delta-seconds
	if secs, err := strconv.Atoi(ra); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}

	// HTTP-date
	if t, err := http.ParseTime(ra); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}

	return 0
}

func flattenHeaders(h http.Header, maxKeys int) map[string]string {
	out := make(map[string]string, minInt(len(h), maxKeys))
	n := 0
	for k, v := range h {
		if n >= maxKeys {
			break
		}
		out[k] = strings.Join(v, ", ")
		n++
	}
	return out
}

func newHTTPClient(timeout time.Duration, maxConnsPerHost int) *http.Client {
	transport := &http.Transport{
		IdleConnTimeout:     90 * time.Second,
		MaxIdleConns:        256,
		MaxIdleConnsPerHost: 64,
		MaxConnsPerHost:     maxConnsPerHost,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}

func readURLs(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out, scanner.Err()
}

func writeJSONLines(w io.Writer, in <-chan logRecord) {
	enc := json.NewEncoder(w)
	for rec := range in {
		_ = enc.Encode(rec)
	}
}

func hashString(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

type sleeper struct {
	rng       *rand.Rand
	base      time.Duration
	jitterMax time.Duration
	sleep     func(d time.Duration)
}

func newSleeper(rng *rand.Rand, base, jitterMax time.Duration, sleep func(d time.Duration)) *sleeper {
	if sleep == nil {
		sleep = time.Sleep
	}
	return &sleeper{
		rng:       rng,
		base:      base,
		jitterMax: jitterMax,
		sleep:     sleep,
	}
}

func (s *sleeper) Sleep() {
	jitter := time.Duration(0)
	if s.jitterMax > 0 {
		jitter = time.Duration(s.rng.Int63n(int64(s.jitterMax) + 1))
	}
	s.sleep(s.base + jitter)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
