package api

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/nesono/evidence-store/internal/model"
)

// parseEvidenceFilter reads the shared evidence filter vocabulary from a query
// string. The list endpoint and the analytics endpoints accept exactly the same
// filters, so they share this parser rather than drifting apart.
//
// Pattern syntax (`~` regex, `*` prefix on procedure_ref, tag containment) is
// interpreted by the store, not here — values are passed through verbatim.
// An empty value means "not set" rather than "match the empty string".
func parseEvidenceFilter(q url.Values) (model.EvidenceFilter, error) {
	filter := model.EvidenceFilter{}

	if v := q.Get("repo"); v != "" {
		filter.Repo = &v
	}
	if v := q.Get("rcs_ref"); v != "" {
		filter.RCSRef = &v
	}
	if v := q.Get("branch"); v != "" {
		filter.Branch = &v
	}
	if v := q.Get("ref"); v != "" {
		filter.Ref = &v
	}
	if v := q.Get("evidence_type"); v != "" {
		filter.EvidenceType = &v
	}
	if v := q.Get("source"); v != "" {
		filter.Source = &v
	}
	if v := q.Get("procedure_ref"); v != "" {
		filter.ProcedureRef = &v
	}
	if v := q.Get("result"); v != "" {
		for _, s := range strings.Split(v, ",") {
			result, err := model.ParseEvidenceResult(strings.TrimSpace(s))
			if err != nil {
				return model.EvidenceFilter{}, err
			}
			filter.Result = append(filter.Result, result)
		}
	}
	if v := q.Get("finished_after"); v != "" {
		t, err := model.ParseFlexibleTime(v)
		if err != nil {
			return model.EvidenceFilter{}, fmt.Errorf("invalid finished_after: %w", err)
		}
		filter.FinishedAfter = &t
	}
	if v := q.Get("finished_before"); v != "" {
		t, err := model.ParseFlexibleTime(v)
		if err != nil {
			return model.EvidenceFilter{}, fmt.Errorf("invalid finished_before: %w", err)
		}
		filter.FinishedBefore = &t
	}
	if v := q.Get("tags"); v != "" {
		filter.Tags = strings.Split(v, ",")
	}
	if v := q.Get("notes"); v != "" {
		filter.Notes = &v
	}

	return filter, nil
}
