package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

// QueryRequest is a message read from the input subject.
type QueryRequest struct {
	Query string `json:"query"`
	// Platforms the query should run on ("darwin", "linux", "windows", ...).
	// An empty list means every platform.
	Platforms []string `json:"platforms"`
}

// QueryResult is a message written to the output subject and the local results file.
type QueryResult struct {
	Host    Host             `json:"host"`
	Query   string           `json:"query"`
	Error   *string          `json:"error"`
	Results []map[string]any `json:"results"`
}

// Agent executes queries received over NATS with osqueryi and publishes the results.
type Agent struct {
	cfg    Config
	hostID string
	out    *GzipJSONLWriter
	nc     *nats.Conn
	log    *slog.Logger
}

func NewAgent(cfg Config, hostID string, out *GzipJSONLWriter, log *slog.Logger) *Agent {
	return &Agent{cfg: cfg, hostID: hostID, out: out, log: log}
}

// Run connects to NATS and processes queries until ctx is cancelled. On shutdown it drains
// the subscription, so a query that is already running finishes and its result is published.
func (a *Agent) Run(ctx context.Context) error {
	closed := make(chan struct{})
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

	nc, err := nats.Connect(a.cfg.NATSURL, opts...)
	if err != nil {
		return fmt.Errorf("connect to NATS: %w", err)
	}
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
	a.log.Info("waiting for queries",
		"url", nc.ConnectedUrlRedacted(),
		"input_subjects", a.cfg.InputSubjects,
		"output_subjects", a.cfg.OutputSubjects,
		"output_file", a.cfg.OutputFile,
		"platform", Platform(),
		"host_id", a.hostID,
	)

	<-ctx.Done()
	a.log.Info("shutting down")
	if err := nc.Drain(); err != nil {
		nc.Close()
	}
	<-closed
	return nil
}

func (a *Agent) handle(msg *nats.Msg) {
	var req QueryRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		a.log.Warn("ignoring malformed query request", "subject", msg.Subject, "error", err)
		return
	}
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		a.log.Warn("ignoring query request without a query", "subject", msg.Subject)
		return
	}
	if !matchesPlatform(req.Platforms, Platform()) {
		a.log.Debug("ignoring query for other platforms", "query", req.Query, "platforms", req.Platforms)
		return
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(a.cfg.QueryTimeout))
	rows, err := ExecuteQuery[map[string]any](ctx, req.Query)
	cancel()

	result := QueryResult{
		Host:    CurrentHost(a.hostID),
		Query:   req.Query,
		Results: rows,
	}
	if err != nil {
		result.Error = ptr(err.Error())
		result.Results = nil
		a.log.Warn("query failed", "query", req.Query, "error", err, "duration", time.Since(start))
	} else {
		a.log.Info("query succeeded", "query", req.Query, "rows", len(rows), "duration", time.Since(start))
	}

	// The local file always gets the full result, even if it is too big to publish.
	if err := a.out.Write(result); err != nil {
		a.log.Error("failed to write result to file", "file", a.cfg.OutputFile, "error", err)
	}
	a.publish(result)
}

// publish sends result to every output subject. A result larger than the server's max
// payload is replaced by an error result so consumers still learn that the query ran.
func (a *Agent) publish(result QueryResult) {
	data, err := a.encodeForNATS(result)
	if err != nil {
		a.log.Error("failed to encode result", "query", result.Query, "error", err)
		return
	}
	for _, subject := range a.cfg.OutputSubjects {
		if err := a.nc.Publish(subject, data); err != nil {
			a.log.Error("failed to publish result", "subject", subject, "query", result.Query, "error", err)
		}
	}
}

func (a *Agent) encodeForNATS(result QueryResult) ([]byte, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if limit := a.nc.MaxPayload(); int64(len(data)) > limit {
		result.Results = nil
		result.Error = ptr(fmt.Sprintf(
			"result is %d bytes, larger than the NATS max payload of %d bytes; full result written to %s on the host",
			len(data), limit, a.cfg.OutputFile))
		return json.Marshal(result)
	}
	return data, nil
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
