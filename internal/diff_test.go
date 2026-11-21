package internal

import (
	"reflect"
	"testing"
)

func TestDiffRows(t *testing.T) {
	for name, tc := range map[string]struct {
		previous, current []map[string]any
		added, removed    []map[string]any
	}{
		"first run reports everything": {
			previous: nil,
			current:  []map[string]any{{"pid": "1"}, {"pid": "2"}},
			added:    []map[string]any{{"pid": "1"}, {"pid": "2"}},
		},
		"no change": {
			previous: []map[string]any{{"pid": "1", "name": "init"}, {"pid": "2", "name": "sh"}},
			current:  []map[string]any{{"name": "sh", "pid": "2"}, {"name": "init", "pid": "1"}}, // reordered
			added:    []map[string]any{},
		},
		"added and removed": {
			previous: []map[string]any{{"pid": "1"}, {"pid": "2"}},
			current:  []map[string]any{{"pid": "2"}, {"pid": "3"}},
			added:    []map[string]any{{"pid": "3"}},
			removed:  []map[string]any{{"pid": "1"}},
		},
		"changed row is removed and added": {
			previous: []map[string]any{{"name": "osquery", "version": "5.1"}},
			current:  []map[string]any{{"name": "osquery", "version": "5.2"}},
			added:    []map[string]any{{"name": "osquery", "version": "5.2"}},
			removed:  []map[string]any{{"name": "osquery", "version": "5.1"}},
		},
		"duplicates are counted": {
			previous: []map[string]any{{"x": "a"}, {"x": "a"}, {"x": "b"}},
			current:  []map[string]any{{"x": "a"}, {"x": "b"}, {"x": "b"}},
			added:    []map[string]any{{"x": "b"}},
			removed:  []map[string]any{{"x": "a"}},
		},
		"everything removed": {
			previous: []map[string]any{{"pid": "1"}},
			current:  []map[string]any{},
			added:    []map[string]any{},
			removed:  []map[string]any{{"pid": "1"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			added, removed := diffRows(tc.previous, tc.current)
			if !reflect.DeepEqual(added, tc.added) {
				t.Errorf("added = %v, want %v", added, tc.added)
			}
			if !reflect.DeepEqual(removed, tc.removed) {
				t.Errorf("removed = %v, want %v", removed, tc.removed)
			}
		})
	}
}

func TestChangeSummary(t *testing.T) {
	for _, tc := range []struct {
		changed, total int
		want           string
	}{
		{5, 20, "5/20 rows changed (25%)"},
		{0, 20, "0/20 rows changed (0%)"},
		{20, 20, "20/20 rows changed (100%)"},
		{1, 3, "1/3 rows changed (33%)"},
		{1, 1000, "1/1000 rows changed (<1%)"},
		{999, 1000, "999/1000 rows changed (>99%)"},
		{0, 0, "0/0 rows changed"},
	} {
		if got := changeSummary(tc.changed, tc.total); got != tc.want {
			t.Errorf("changeSummary(%d, %d) = %q, want %q", tc.changed, tc.total, got, tc.want)
		}
	}
}
