package store

import (
	"fmt"
	"strings"

	"github.com/nesono/evidence-store/internal/model"
)

// sqlFilter accumulates WHERE conditions and their bind arguments. It is shared
// by the list and analytics queries so both interpret the filter vocabulary —
// `~` regex, `*` prefix on procedure_ref, tag containment — identically.
//
// Callers may keep appending conditions after applying a filter; argument
// numbering continues from wherever the filter left off.
type sqlFilter struct {
	where []string
	args  []any
}

// arg registers a bind argument and returns its placeholder.
func (f *sqlFilter) arg(v any) string {
	f.args = append(f.args, v)
	return fmt.Sprintf("$%d", len(f.args))
}

// add appends a raw condition. The caller is responsible for binding any values
// through arg rather than interpolating them.
func (f *sqlFilter) add(condition string) {
	f.where = append(f.where, condition)
}

// whereClause renders the accumulated conditions, including the leading
// " WHERE ", or the empty string when there are none.
func (f *sqlFilter) whereClause() string {
	if len(f.where) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(f.where, " AND ")
}

// buildFilter translates an EvidenceFilter into SQL conditions.
func buildFilter(filter model.EvidenceFilter) *sqlFilter {
	f := &sqlFilter{}

	// A leading `~` switches an exact match to a POSIX regex match.
	textFilter := func(column string, v *string) {
		if v == nil {
			return
		}
		val := *v
		if strings.HasPrefix(val, "~") {
			f.add(fmt.Sprintf("%s ~ %s", column, f.arg(val[1:])))
		} else {
			f.add(fmt.Sprintf("%s = %s", column, f.arg(val)))
		}
	}

	textFilter("repo", filter.Repo)
	textFilter("rcs_ref", filter.RCSRef)
	textFilter("branch", filter.Branch)
	textFilter("evidence_type", filter.EvidenceType)
	textFilter("source", filter.Source)

	// procedure_ref additionally supports a trailing `*` for prefix matching,
	// which is how callers select a whole Bazel package.
	if v := filter.ProcedureRef; v != nil {
		val := *v
		if strings.HasPrefix(val, "~") {
			f.add(fmt.Sprintf("procedure_ref ~ %s", f.arg(val[1:])))
		} else if strings.HasSuffix(val, "*") {
			prefix := strings.TrimSuffix(val, "*")
			f.add(fmt.Sprintf("procedure_ref LIKE %s", f.arg(prefix+"%")))
		} else {
			f.add(fmt.Sprintf("procedure_ref = %s", f.arg(val)))
		}
	}

	if len(filter.Result) > 0 {
		placeholders := make([]string, len(filter.Result))
		for i, r := range filter.Result {
			placeholders[i] = f.arg(r)
		}
		f.add(fmt.Sprintf("result IN (%s)", strings.Join(placeholders, ",")))
	}

	if v := filter.FinishedAfter; v != nil {
		f.add(fmt.Sprintf("finished_at >= %s", f.arg(*v)))
	}
	if v := filter.FinishedBefore; v != nil {
		f.add(fmt.Sprintf("finished_at < %s", f.arg(*v)))
	}

	if len(filter.Tags) > 0 {
		// If the first tag starts with ~, treat all tags as regex patterns.
		if strings.HasPrefix(filter.Tags[0], "~") {
			for _, t := range filter.Tags {
				pattern := strings.TrimPrefix(t, "~")
				f.add(fmt.Sprintf(
					"EXISTS (SELECT 1 FROM jsonb_array_elements_text(metadata->'tags') tag WHERE tag ~ %s)",
					f.arg(pattern)))
			}
		} else {
			f.add(fmt.Sprintf("metadata->'tags' @> %s", f.arg(mustJSON(filter.Tags))))
		}
	}

	if v := filter.Notes; v != nil {
		val := *v
		if strings.HasPrefix(val, "~") {
			f.add(fmt.Sprintf("metadata->>'notes' ~ %s", f.arg(val[1:])))
		} else {
			f.add(fmt.Sprintf("metadata->>'notes' = %s", f.arg(val)))
		}
	}

	return f
}
