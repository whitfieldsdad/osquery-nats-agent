package internal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"container/list"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// QueryRequest is a request to run a query, read from an input subject or an input file.
type QueryRequest struct {
	Query string `json:"query"`
	// Platforms the query should run on ("darwin", "linux", "windows", ...).
	// An empty list means every platform.
	Platforms []string `json:"platforms"`
}

// QueryResult is a message written to the output subject and the local results file.
type QueryResult struct {
	Host Host `json:"host"`
	// Date and Time are when the query finished, in UTC: the date (YYYY-MM-DD) and the
	// timestamp (YYYY-MM-DDTHH:MM:SS), in the same formats as the {{ date }} and {{ time }}
	// template variables.
	Date string `json:"date"`
	Time string `json:"time"`
	// Name is the scheduled query's name; it is omitted for queries received over NATS or
	// read from input files.
	Name  string  `json:"name,omitempty"`
	Query string  `json:"query"`
	Error *string `json:"error"`
	// Results holds the rows returned by the query that are new or changed since its previous
	// successful run (every row, on its first run). With Config.Snapshot set, it holds every
	// row the query returned.
	Results []map[string]any `json:"results"`
	// Removed holds the rows that were returned by the query's previous successful run but
	// not by this one. A changed row appears in both Removed (old) and Results (new). It is
	// always empty with Config.Snapshot set.
	Removed []map[string]any `json:"removed,omitempty"`
}

// errNotQueryRequest is returned by ParseQueryRequest for anything but a JSON object.
var errNotQueryRequest = errors.New(`query request must be a JSON object, e.g. {"query": "SELECT 1;"}`)

// ParseQueryRequest decodes a query request, a JSON QueryRequest object, as read from an
// input subject or an input file. The returned query is trimmed of surrounding whitespace.
func ParseQueryRequest(data []byte) (QueryRequest, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return QueryRequest{}, errNotQueryRequest
	}
	var req QueryRequest
	if err := json.Unmarshal(trimmed, &req); err != nil {
		return QueryRequest{}, err
	}
	req.Query = strings.TrimSpace(req.Query)
	return req, nil
}

// Agent executes queries received over NATS, read from input files and scheduled in its
// config with osqueryi, and delivers the results. By default they are delivered
// differentially: each run of a query delivers only the rows that changed since its previous
// successful run. With cfg.Snapshot set, each run delivers every row.
type Agent struct {
	cfg    Config
	hostID string
	out    GzipJSONLWriters
	nc     *nats.Conn // nil if there are no input or output subjects
	log    *slog.Logger
	now    func() time.Time
	// query runs a query; nil means ExecuteQuery (osqueryi).
	query func(ctx context.Context, query string) ([]map[string]any, error)

	// baselines holds the previous result of each query received over NATS or read from an
	// input file, by its SQL, for up to cfg.MaxCachedQueries queries; baselineLRU orders them from
	// most to least recently run. Each scheduled query has a baseline of its own.
	baselinesMu sync.Mutex
	baselines   map[string]*list.Element // of *baselineEntry in baselineLRU
	baselineLRU list.List

	// outputPaths holds the absolute path of every output file written to (or opened), which
	// are never read as input files.
	outputPathsMu sync.Mutex
	outputPaths   map[string]bool
}

type baselineEntry struct {
	query string
	b     *baseline
}

// baseline is the result of a query's previous successful run, which its next run is
// compared with. mu is held while the query runs, so runs of the same query do not overlap.
type baseline struct {
	mu   sync.Mutex
	rows []map[string]any
}

func NewAgent(cfg Config, hostID string, out GzipJSONLWriters, log *slog.Logger) *Agent {
	return &Agent{cfg: cfg, hostID: hostID, out: out, log: log, now: time.Now}
}

// Run processes queries until ctx is cancelled: those received on the input subjects, the
// scheduled ones, and those in the input files, which are polled for new queries
// (cfg.InputFiles must already hold rendered glob patterns, as RenderInputFiles returns).
// It connects to NATS only if there are input or output subjects. On shutdown, queries that
// are already running finish and their results are delivered before the connection closes.
func (a *Agent) Run(ctx context.Context) error {
	a.rememberOutputPaths(a.out.Paths())
	var closed chan struct{}
	if len(a.cfg.InputSubjects) > 0 || len(a.cfg.OutputSubjects) > 0 {
		closed = make(chan struct{})
		if err := a.connect(closed); err != nil {
			return err
		}
	}
	a.log.Info("waiting for queries",
		"input_subjects", a.cfg.InputSubjects,
		"output_subjects", a.cfg.OutputSubjects,
		"output_files", a.out.Paths(),
		"snapshot", a.cfg.Snapshot,
		"platform", Platform(),
		"host_id", a.hostID,
	)

	var schedules sync.WaitGroup
	if len(a.cfg.InputFiles) > 0 {
		a.log.Info("polling input files",
			"input_files", a.cfg.InputFiles,
			"input_file_poll_interval", time.Duration(a.cfg.InputFilePollInterval),
		)
		schedules.Add(1)
		go func() {
			defer schedules.Done()
			a.pollInputFiles(ctx)
		}()
	}
	for _, sq := range a.cfg.ScheduledQueries {
		if !matchesPlatform(sq.Platforms, Platform()) {
			a.log.Info("not scheduling query for other platforms", "name", sq.Name, "platforms", sq.Platforms)
			continue
		}
		a.log.Info("scheduling query", "name", sq.Name, "interval", time.Duration(sq.Interval)*time.Second)
		schedules.Add(1)
		go func() {
			defer schedules.Done()
			a.runSchedule(ctx, sq)
		}()
	}

	<-ctx.Done()
	a.log.Info("shutting down")
	schedules.Wait() // let running scheduled and input file queries finish and deliver
	if a.nc != nil {
		if err := a.nc.Drain(); err != nil {
			a.nc.Close()
		}
		<-closed
	}
	return nil
}

// connect connects to NATS and subscribes to the input subjects, setting a.nc. closed is
// closed once the connection has closed.
func (a *Agent) connect(closed chan struct{}) error {
	opts := []nats.Option{
		nats.Name("osquery-nats-sidecar/" + a.hostID),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2 * time.Second),
		// Allow a running query to finish (and publish) before the connection closes.
		nats.DrainTimeout(time.Duration(a.cfg.QueryTimeout) + 10*time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				a.log.Warn("disconnected from NATS", "error", err)
			}
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			a.log.Info("reconnected to NATS", "url", nc.ConnectedUrlRedacted())
		}),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			a.log.Error("NATS error", "error", err)
		}),
		nats.ClosedHandler(func(*nats.Conn) { close(closed) }),
	}
	if a.cfg.NATSCredentialsPath != "" {
		opts = append(opts, nats.UserCredentials(a.cfg.NATSCredentialsPath))
	}
	tlsConfig, err := NATSTLSConfig(a.cfg)
	if err != nil {
		return err
	}
	if tlsConfig != nil {
		opts = append(opts, nats.Secure(tlsConfig))
	}

	nc, err := nats.Connect(a.cfg.NATSURL, opts...)
	if err != nil {
		return fmt.Errorf("connect to NATS: %w", err)
	}
	// Set before subscribing, so that a query received straight away can publish its result
	// (and so that subscribing orders this write before the handlers' reads).
	a.nc = nc

	// No queue group: every agent subscribed to an input subject runs every query. Each
	// subscription delivers its messages in order, one at a time; queries arriving on
	// different input subjects may run concurrently.
	for _, subject := range a.cfg.InputSubjects {
		if _, err := nc.Subscribe(subject, a.handle); err != nil {
			nc.Close()
			return fmt.Errorf("subscribe to %q: %w", subject, err)
		}
	}
	if err := nc.Flush(); err != nil {
		nc.Close()
		return fmt.Errorf("subscribe: %w", err)
	}
	a.log.Info("connected to NATS", "url", nc.ConnectedUrlRedacted())
	return nil
}

func (a *Agent) handle(msg *nats.Msg) {
	req, err := ParseQueryRequest(msg.Data)
	if err != nil {
		a.log.Warn("ignoring malformed query request", "subject", msg.Subject, "error", err)
		return
	}
	a.runRequest(req, "subject", msg.Subject)
}

// inputFile is what the agent knows about an input file it has read.
type inputFile struct {
	info os.FileInfo // the file as it was just before it was last read; nil if never read
	// sum is the SHA-256 of the first info.Size() bytes of a JSONL file, as they were when it
	// was last read. If they have changed by the next read, the file has been rewritten.
	sum  []byte
	line int // the number of the last line read from a JSONL file
	// requests holds requestSum of each request read from a JSON file, in order.
	requests [][sha256.Size]byte
	// output is set if the file is one of the agent's output files, which is never read.
	output bool
	// pending is set when the file ended with a line without a trailing newline, which is
	// left unread until the file has gone a poll without changing.
	pending bool
}

// pollInputFiles polls the input files for new queries until ctx is cancelled, waiting a
// cfg.InputFilePollInterval after each poll finishes.
func (a *Agent) pollInputFiles(ctx context.Context) {
	files := make(map[string]*inputFile)
	unmatched := make(map[string]bool)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		a.pollInputFilesOnce(ctx, files, unmatched)
		timer.Reset(time.Duration(a.cfg.InputFilePollInterval))
	}
}

// pollInputFilesOnce matches the input file patterns and runs the queries in each file that
// is new or has grown since the previous poll, from the line after the last one read.
// files holds what is known about each file, and unmatched the patterns already reported as
// matching no files; both are updated.
//
// A file that has been replaced (it is a different file, or smaller than before) or
// rewritten (the bytes already read have changed; see readInputFile) is read from the start.
// A file whose size and modification time are unchanged is not read again. A file that is
// no longer matched is forgotten, so it is read from the start if it is matched again.
func (a *Agent) pollInputFilesOnce(ctx context.Context, files map[string]*inputFile, unmatched map[string]bool) {
	paths, noMatches, err := GlobInputFiles(a.cfg.InputFiles)
	if err != nil {
		a.log.Error("failed to match input files", "error", err)
	}
	nowUnmatched := make(map[string]bool, len(noMatches))
	for _, pattern := range noMatches {
		nowUnmatched[pattern] = true
		if !unmatched[pattern] {
			a.log.Warn("input file pattern matches no files", "pattern", pattern)
		}
	}
	clear(unmatched)
	maps.Copy(unmatched, nowUnmatched)

	matched := make(map[string]bool, len(paths))
	for _, path := range paths {
		matched[path] = true
		if ctx.Err() != nil {
			return
		}
		info, err := os.Stat(path)
		if err != nil {
			a.log.Warn("failed to stat input file", "input_file", path, "error", err)
			delete(files, path)
			continue
		}
		if info.IsDir() {
			continue
		}

		f := files[path]
		if f == nil {
			f = &inputFile{output: a.isOutputPath(path)}
			files[path] = f
			if f.output {
				a.log.Warn("not reading an output file as an input file", "input_file", path)
			}
		}
		if f.output {
			continue
		}
		if isJSONInputFile(path) {
			// A JSON file is read whole whenever it changes. Which of its requests have already
			// run is judged by their content, so it does not matter whether it was edited in
			// place or replaced.
			if f.info == nil || !os.SameFile(f.info, info) || info.Size() != f.info.Size() ||
				!info.ModTime().Equal(f.info.ModTime()) {
				a.readJSONInputFile(ctx, path, f, info)
			}
			continue
		}

		stable := false
		switch {
		case f.info == nil:
			// Never read successfully.
		case !os.SameFile(f.info, info) || info.Size() < f.info.Size():
			a.log.Info("input file was replaced; reading it from the start", "input_file", path)
			*f = inputFile{}
		case info.Size() == f.info.Size() && info.ModTime().Equal(f.info.ModTime()):
			if !f.pending {
				continue // unchanged since it was last read
			}
			stable = true
		}
		a.readInputFile(ctx, path, f, info, stable)
	}
	for path := range files {
		if !matched[path] {
			delete(files, path)
		}
	}
}

// readInputFile runs the queries in the input file at path that follow line number f.line,
// in order, stopping early if ctx is cancelled, and updates f. info is the file's current
// FileInfo. Malformed lines are logged and skipped. A final line without a trailing newline
// is run only if unterminated is true.
//
// Before any query is run, the SHA-256 of the bytes already read (the first f.info.Size())
// is compared with f.sum. If they differ, the file has been rewritten rather than appended
// to, and it is read from the start.
func (a *Agent) readInputFile(ctx context.Context, path string, f *inputFile, info os.FileInfo, unterminated bool) {
	log := a.log.With("input_file", path)
	file, err := os.Open(path)
	if err != nil {
		log.Error("failed to open input file", "error", err)
		return
	}
	defer file.Close()

	var readSize int64
	if f.info != nil {
		readSize = f.info.Size()
	}
	readSum, sum, err := hashPrefixes(file, readSize, info.Size())
	if err != nil {
		// Most likely the file was truncated since it was stat'ed; the next poll will see
		// that it is smaller.
		log.Error("failed to read input file", "error", err)
		return
	}
	if f.sum != nil && !bytes.Equal(readSum, f.sum) {
		log.Info("input file was rewritten; reading it from the start", "line", f.line)
		f.line, f.pending, unterminated = 0, false, false
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		log.Error("failed to read input file", "error", err)
		return
	}

	after := f.line
	log.Debug("reading queries from input file", "after_line", after)
	queries := 0
	last, pending, err := readQueryRequests(file, after, unterminated, func(line int, req QueryRequest, err error) error {
		if err != nil {
			log.Warn("ignoring malformed query request", "line", line, "error", err)
			return nil
		}
		if a.runRequest(req, "input_file", path, "line", line) {
			queries++
		}
		return ctx.Err()
	})
	switch {
	case errors.Is(err, context.Canceled):
		log.Info("stopped reading input file on shutdown", "queries", queries, "line", last)
	case err != nil:
		log.Error("failed to read input file", "queries", queries, "line", last, "error", err)
	case queries > 0 || last > after:
		log.Info("read input file", "queries", queries, "line", last)
	}
	if pending {
		log.Debug("input file ends with an incomplete line; waiting for it to stop changing", "line", last+1)
	}
	f.info, f.sum, f.line, f.pending = info, sum, max(last, after), pending
}

// readJSONInputFile runs the new query requests in the JSON input file at path, in order,
// stopping early if ctx is cancelled, and updates f. info is the file's current FileInfo.
// Malformed requests are logged and skipped. A file that is not valid JSON, perhaps because
// it is still being written, is left until it next changes.
//
// The requests in f.requests have already been read. If they are unchanged, only the
// requests after them are run (none, if requests were only removed from the end); otherwise
// the file has been rewritten and every request in it is run.
func (a *Agent) readJSONInputFile(ctx context.Context, path string, f *inputFile, info os.FileInfo) {
	log := a.log.With("input_file", path)
	file, err := os.Open(path)
	if err != nil {
		log.Error("failed to open input file", "error", err)
		return
	}
	raws, err := readJSONQueryRequests(file)
	file.Close()
	f.info = info
	if err != nil {
		log.Warn("input file is not valid JSON; it will be read again when it changes", "error", err)
		return
	}

	sums := make([][sha256.Size]byte, len(raws))
	for i, raw := range raws {
		sums[i] = requestSum(raw)
	}
	after := min(len(f.requests), len(raws))
	for i := range after {
		if sums[i] != f.requests[i] {
			log.Info("input file was rewritten; reading it from the start", "requests", len(f.requests))
			after = 0
			break
		}
	}
	log.Debug("reading queries from input file", "after_request", after)
	read, queries := after, 0
	for i := after; i < len(raws); i++ {
		if ctx.Err() != nil {
			log.Info("stopped reading input file on shutdown", "queries", queries, "request", read)
			break
		}
		req, err := ParseQueryRequest(raws[i])
		if err != nil {
			log.Warn("ignoring malformed query request", "request", i+1, "error", err)
		} else if a.runRequest(req, "input_file", path, "request", i+1) {
			queries++
		}
		read = i + 1
	}
	if queries > 0 || read > after {
		log.Info("read input file", "queries", queries, "requests", read)
	}
	f.requests = sums[:read]
}

// hashPrefixes returns the SHA-256 of the first n bytes of r and of its first size bytes,
// reading size bytes from r. n must not be greater than size.
func hashPrefixes(r io.Reader, n, size int64) (nSum, sizeSum []byte, err error) {
	h := sha256.New()
	if _, err := io.CopyN(h, r, n); err != nil {
		return nil, nil, err
	}
	nSum = h.Sum(nil)
	if _, err := io.CopyN(h, r, size-n); err != nil {
		return nil, nil, err
	}
	return nSum, h.Sum(nil), nil
}

// runRequest runs req if it has a query and targets this platform, reporting whether it
// ran. source describes where req came from, as log attributes.
func (a *Agent) runRequest(req QueryRequest, source ...any) bool {
	if req.Query == "" {
		a.log.Warn("ignoring query request without a query", source...)
		return false
	}
	if !matchesPlatform(req.Platforms, Platform()) {
		a.log.Debug("ignoring query for other platforms",
			append(source, "query", req.Query, "platforms", req.Platforms)...)
		return false
	}
	log := a.log.With(append(source, "query", req.Query)...)
	if a.cfg.Snapshot {
		a.runSnapshot("", req.Query, log)
	} else {
		a.runDiff(a.baselineFor(req.Query), "", req.Query, log)
	}
	return true
}

// baselineFor returns the baseline of a query received over NATS or read from an input
// file, dropping the least recently used baseline if there are more than
// cfg.MaxCachedQueries (DefaultMaxCachedQueries if unset).
func (a *Agent) baselineFor(query string) *baseline {
	a.baselinesMu.Lock()
	defer a.baselinesMu.Unlock()
	if e := a.baselines[query]; e != nil {
		a.baselineLRU.MoveToFront(e)
		return e.Value.(*baselineEntry).b
	}
	if a.baselines == nil {
		a.baselines = make(map[string]*list.Element)
	}
	b := &baseline{}
	a.baselines[query] = a.baselineLRU.PushFront(&baselineEntry{query: query, b: b})
	limit := a.cfg.MaxCachedQueries
	if limit <= 0 {
		limit = DefaultMaxCachedQueries
	}
	if a.baselineLRU.Len() > limit {
		oldest := a.baselineLRU.Remove(a.baselineLRU.Back()).(*baselineEntry)
		delete(a.baselines, oldest.query)
	}
	return b
}

// rememberOutputPaths records paths as output files, which are never read as input files.
func (a *Agent) rememberOutputPaths(paths []string) {
	a.outputPathsMu.Lock()
	defer a.outputPathsMu.Unlock()
	for _, path := range paths {
		if abs, err := filepath.Abs(path); err == nil {
			if a.outputPaths == nil {
				a.outputPaths = make(map[string]bool)
			}
			a.outputPaths[abs] = true
		}
	}
}

// isOutputPath reports whether path is one of the output files written to so far.
func (a *Agent) isOutputPath(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	a.outputPathsMu.Lock()
	defer a.outputPathsMu.Unlock()
	return a.outputPaths[abs]
}

// runSchedule runs sq immediately and then every sq.Interval seconds until ctx is cancelled.
// Runs never overlap: if a run takes longer than the interval, the next one starts as soon
// as it finishes.
func (a *Agent) runSchedule(ctx context.Context, sq ScheduledQuery) {
	log := a.log.With("name", sq.Name, "query", sq.Query)
	ticker := time.NewTicker(time.Duration(sq.Interval) * time.Second)
	defer ticker.Stop()

	var b baseline
	for {
		if a.cfg.Snapshot {
			a.runSnapshot(sq.Name, sq.Query, log)
		} else {
			a.runDiff(&b, sq.Name, sq.Query, log)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// runDiff runs query and delivers the rows added and removed since b's previous successful
// run (every row, on the first run), then makes this run's rows the new baseline. A run in
// which no rows changed delivers nothing. A failed run delivers an error result and leaves
// the baseline as it was. So does a run whose result could not be delivered in full, so
// that its changes are delivered again, with any later ones, by the next run: results are
// delivered at least once. name is the scheduled query's name, if any.
func (a *Agent) runDiff(b *baseline, name, query string, log *slog.Logger) {
	b.mu.Lock()
	defer b.mu.Unlock()

	start := time.Now()
	rows, err := a.execute(query)
	if err != nil {
		log.Warn("query failed", "error", err, "duration", time.Since(start))
		a.deliver(a.newResult(name, query, nil, err))
		return
	}
	added, removed := diffRows(b.rows, rows)
	changed, unchanged := len(added)+len(removed), len(rows)-len(added)
	log.Info("query succeeded",
		"changes", changeSummary(changed, changed+unchanged),
		"rows", len(rows),
		"added", len(added),
		"removed", len(removed),
		"unchanged", unchanged,
		"duration", time.Since(start),
	)
	if changed == 0 {
		b.rows = rows
		return
	}
	result := a.newResult(name, query, added, nil)
	result.Removed = removed
	if !a.deliver(result) {
		log.Warn("result was not fully delivered; its changes will be delivered again by the next run")
		return
	}
	b.rows = rows
}

// runSnapshot runs query and delivers every row it returned, even when there are none, so
// that each result is complete on its own. A failed run delivers an error result. A result
// that could not be delivered in full is not retried, as the next run delivers a newer
// snapshot. name is the scheduled query's name, if any.
func (a *Agent) runSnapshot(name, query string, log *slog.Logger) {
	start := time.Now()
	rows, err := a.execute(query)
	if err != nil {
		log.Warn("query failed", "error", err, "duration", time.Since(start))
		a.deliver(a.newResult(name, query, nil, err))
		return
	}
	if rows == nil {
		rows = []map[string]any{} // encode as [], not null
	}
	log.Info("query succeeded", "rows", len(rows), "duration", time.Since(start))
	if !a.deliver(a.newResult(name, query, rows, nil)) {
		log.Warn("result was not fully delivered")
	}
}

// changeSummary describes how many of total rows changed, e.g. "5/20 rows changed (25%)".
// The percentage is rounded, but never to 0% or 100% unless it is exactly that.
func changeSummary(changed, total int) string {
	if total == 0 {
		return "0/0 rows changed"
	}
	pct := fmt.Sprintf("%.0f%%", 100*float64(changed)/float64(total))
	switch {
	case changed > 0 && pct == "0%":
		pct = "<1%"
	case changed < total && pct == "100%":
		pct = ">99%"
	}
	return fmt.Sprintf("%d/%d rows changed (%s)", changed, total, pct)
}

// execute runs query, bounded by the configured query timeout.
func (a *Agent) execute(query string) ([]map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(a.cfg.QueryTimeout))
	defer cancel()
	if a.query != nil {
		return a.query(ctx, query)
	}
	return ExecuteQuery[map[string]any](ctx, query)
}

func (a *Agent) newResult(name, query string, rows []map[string]any, err error) QueryResult {
	now := a.now().UTC()
	result := QueryResult{
		Host:    CurrentHost(a.hostID),
		Date:    now.Format(DateFormat),
		Time:    now.Format(TimeFormat),
		Name:    name,
		Query:   query,
		Results: rows,
	}
	if err != nil {
		result.Error = ptr(err.Error())
		result.Results = nil
	}
	return result
}

// deliver appends result to the output files and publishes it to the output subjects,
// reporting whether every one of them received it.
func (a *Agent) deliver(result QueryResult) bool {
	// The local files always get the full result, even if it is too big to publish.
	paths, err := a.out.Write(result)
	a.rememberOutputPaths(paths)
	ok := err == nil
	if err != nil {
		a.log.Error("failed to write result to file", "query", result.Query, "error", err)
	}
	return a.publish(result, paths) && ok
}

// publish sends result to every output subject, if there are any, reporting whether it was
// published everywhere. A result larger than the server's max payload is split across
// several messages (see encodeForNATS).
func (a *Agent) publish(result QueryResult, paths []string) bool {
	if a.nc == nil || len(a.cfg.OutputSubjects) == 0 {
		return true
	}
	msgs, dropped, err := encodeForNATS(result, paths, a.nc.MaxPayload())
	if err != nil {
		a.log.Error("failed to encode result", "query", result.Query, "error", err)
		return false
	}
	if len(msgs) > 1 {
		a.log.Debug("result split across messages", "query", result.Query, "messages", len(msgs))
	}
	if dropped > 0 {
		a.log.Warn("rows too large to publish were replaced by an error", "query", result.Query,
			"rows", dropped, "output_files", paths)
	}
	ok := true
	for _, subject := range a.cfg.OutputSubjects {
		for _, data := range msgs {
			if err := a.nc.Publish(subject, data); err != nil {
				a.log.Error("failed to publish result", "subject", subject, "query", result.Query, "error", err)
				ok = false
				break
			}
		}
	}
	return ok
}

// encodeForNATS encodes result as messages of at most limit bytes. A result that is too
// large is split into several, each with the same metadata and some of its rows. Each
// message is a complete result, so they can be processed independently.
//
// A row too large to fit in a message on its own is dropped, and an error result naming
// paths, the files the full result was written to, is sent in its place, so consumers
// still learn that the query ran. dropped is the number of rows dropped.
func encodeForNATS(result QueryResult, paths []string, limit int64) (msgs [][]byte, dropped int, err error) {
	data, err := json.Marshal(result)
	if err != nil {
		return nil, 0, err
	}
	if int64(len(data)) <= limit {
		return [][]byte{data}, 0, nil
	}
	rows := len(result.Results) + len(result.Removed)
	if rows <= 1 {
		msg := fmt.Sprintf("result is %d bytes, larger than the NATS max payload of %d bytes", len(data), limit)
		if len(paths) > 0 {
			msg += "; full result written to " + strings.Join(paths, ", ") + " on the host"
		}
		result.Results, result.Removed, result.Error = nil, nil, &msg
		data, err := json.Marshal(result)
		return [][]byte{data}, rows, err
	}

	first, second := result, result
	k := rows / 2
	if k <= len(result.Results) {
		first.Results, first.Removed = result.Results[:k], nil
		second.Results = result.Results[k:]
	} else {
		first.Removed = result.Removed[:k-len(result.Results)]
		second.Results, second.Removed = []map[string]any{}, result.Removed[k-len(result.Results):]
	}
	for _, half := range []QueryResult{first, second} {
		m, d, err := encodeForNATS(half, paths, limit)
		if err != nil {
			return nil, 0, err
		}
		msgs, dropped = append(msgs, m...), dropped+d
	}
	return msgs, dropped, nil
}

func matchesPlatform(platforms []string, platform string) bool {
	if len(platforms) == 0 {
		return true
	}
	for _, p := range platforms {
		if strings.EqualFold(strings.TrimSpace(p), platform) {
			return true
		}
	}
	return false
}

func ptr[T any](v T) *T { return &v }
