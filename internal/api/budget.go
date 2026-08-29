package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// A budget bounds how long one query may run.
//
// Without one, an expensive query simply runs until the server's request
// timeout drops the connection, and the caller gets no response at all — no
// status, no body, nothing to act on. Failing early with advice is strictly
// more useful than a socket that closes.
//
// Analytics had this first, because an unfiltered aggregation over a year is
// obviously expensive. Search needs it for a less obvious reason: a leading `~`
// on a filter hands a POSIX regex to Postgres (see store.buildFilter), and a
// pattern can be made to cost far more than the table it runs over. The
// pattern is bound as an argument so there is nothing to inject — it is the
// evaluation that is worth bounding, not the SQL.
//
// So both share this, and share the wording, because a caller who has hit one
// should recognise the other.
func queryBudget(r *http.Request, limit time.Duration) (context.Context, context.CancelFunc, func(error) bool) {
	if limit <= 0 {
		return r.Context(), func() {}, func(error) bool { return false }
	}

	ctx, cancel := context.WithTimeout(r.Context(), limit)
	// Reports whether the budget is what expired, as opposed to the caller
	// having gone away — which is not an error worth reporting to somebody who
	// is no longer listening.
	exceeded := func(err error) bool {
		return errors.Is(err, context.DeadlineExceeded) && r.Context().Err() == nil
	}
	return ctx, cancel, exceeded
}

func writeBudgetExceeded(w http.ResponseWriter, limit time.Duration) {
	writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf(
		"query did not finish within %s; narrow the filter or the time window", limit))
}
