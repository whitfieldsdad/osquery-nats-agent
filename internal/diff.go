package internal

import "encoding/json"

// diffRows compares two query results and returns the rows in current that are not in
// previous (added) and the rows in previous that are not in current (removed). Rows are
// compared by all of their columns, so a row whose values changed is reported as removed
// (old values) and added (new values). Duplicate rows are counted, so a row that appears
// twice now and once before is reported as added once.
//
// added is never nil, so it encodes as [] rather than null; removed is nil when empty.
func diffRows(previous, current []map[string]any) (added, removed []map[string]any) {
	remaining := make(map[string]int, len(previous))
	for _, row := range previous {
		remaining[rowKey(row)]++
	}

	added = []map[string]any{}
	for _, row := range current {
		key := rowKey(row)
		if remaining[key] > 0 {
			remaining[key]--
		} else {
			added = append(added, row)
		}
	}

	for _, row := range previous {
		key := rowKey(row)
		if remaining[key] > 0 {
			remaining[key]--
			removed = append(removed, row)
		}
	}
	return added, removed
}

// rowKey returns a canonical encoding of row. encoding/json writes map keys in sorted order,
// so equal rows always produce the same key.
func rowKey(row map[string]any) string {
	b, err := json.Marshal(row)
	if err != nil {
		// Rows decoded from osqueryi's JSON output always re-encode; fall back to something
		// that still distinguishes rows rather than treating them all as equal.
		return "\x00" + err.Error()
	}
	return string(b)
}
