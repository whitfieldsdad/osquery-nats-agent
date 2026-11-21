package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// OsqueryBinary is the osqueryi executable to run. Override it if osqueryi is not on PATH.
var OsqueryBinary = "osqueryi"

// ExecuteQuery runs query with `osqueryi --json` and decodes each result row into a T.
// The osqueryi process is killed if ctx is cancelled.
//
// T is usually a struct with json tags matching the selected columns. osqueryi emits every
// column value as a JSON string (e.g. {"pid":"1"}), so numeric fields need the ",string"
// option:
//
//	type Process struct {
//		PID  int64  `json:"pid,string"`
//		Name string `json:"name"`
//	}
//
// Use map[string]string as T to get untyped rows.
func ExecuteQuery[T any](ctx context.Context, query string) ([]T, error) {
	cmd := exec.CommandContext(ctx, OsqueryBinary, "--json", query)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("osqueryi: %w", ctxErr)
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("osqueryi: %w: %s", err, msg)
		}
		return nil, fmt.Errorf("osqueryi: %w", err)
	}

	var results []T
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		return nil, fmt.Errorf("osqueryi: decode output: %w", err)
	}
	return results, nil
}
