package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nesono/evidence-store/internal/campaign"
)

// Closing is one transaction, including every generated skip. The execution
// lock is also acquired by edits/finalization/start, so unfinished work cannot
// slip in between the scan and closure. Revision checks include draft changes.
func (s *CampaignStore) SetExecutionClosed(ctx context.Context, id uuid.UUID, revision int64, closed bool, actor string) (*CampaignExecution, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	e, err := scanExecution(tx.QueryRow(ctx, `SELECT `+executionColumns+` FROM campaign_execution WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return nil, err
	}
	if e.Revision != revision || (e.ClosedAt != nil) == closed {
		return nil, ErrCampaignConflict
	}
	if closed {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, campaignSweepLock); err != nil {
			return nil, err
		}
		if err := s.closeUnfinished(ctx, tx, e, actor); err != nil {
			return nil, err
		}
	}
	e, err = scanExecution(tx.QueryRow(ctx, `UPDATE campaign_execution SET revision=revision+1,updated_at=now(),
 closed_at=CASE WHEN $2 THEN now() ELSE NULL END,closed_by=CASE WHEN $2 THEN $3 ELSE NULL END
 WHERE id=$1 RETURNING `+executionColumns, id, closed, actor))
	if err != nil {
		return nil, err
	}
	action := "reopened"
	if closed {
		action = "closed"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO campaign_execution_event(execution_id,revision,action,actor) VALUES($1,$2,$3,$4)`, id, e.Revision, action, actor); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetExecution(ctx, id)
}

func (s *CampaignStore) closeUnfinished(ctx context.Context, tx pgx.Tx, e *CampaignExecution, actor string) error {
	p, err := scanCampaign(tx.QueryRow(ctx, `SELECT `+campaignColumns+` FROM campaign_package WHERE id=$1`, e.PackageID))
	if err != nil {
		return err
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return err
	}
	for i, scenario := range p.Definition.Scenarios {
		ref := scenario.Definition.ProcedureRef
		var latestID *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT (SELECT id FROM campaign_attempt WHERE execution_id=$1 AND procedure_ref=$2
    ORDER BY (finalized_at IS NULL) DESC,created_at DESC,id DESC LIMIT 1)`, e.ID, ref).Scan(&latestID); err != nil {
			return err
		}
		var a *CampaignAttempt
		if latestID != nil {
			a, err = scanAttempt(tx.QueryRow(ctx, `SELECT `+attemptColumns+` FROM campaign_attempt WHERE id=$1 FOR UPDATE`, latestID))
			if err != nil {
				return err
			}
			if a.FinalizedAt != nil {
				continue
			}
		} else {
			data := campaign.InitialDraft(scenario.Procedure)
			data.Repo, data.Branch, data.RCSRef = e.Repo, e.Branch, e.RCSRef
			data.Source = actor
			raw, err := json.Marshal(data)
			if err != nil {
				return err
			}
			a, err = scanAttempt(tx.QueryRow(ctx, `INSERT INTO campaign_attempt(id,campaign_id,execution_id,procedure_ref,owner,data)
    VALUES($1,$2,$3,$4,$5,$6) RETURNING `+attemptColumns, uuid.New(), e.PackageID, e.ID, ref, actor, raw))
			if err != nil {
				return err
			}
		}
		// Only missing outcomes are supplied. Preserve observations, measurements and
		// already-performed checks even though the unfinished attempt is disposed of.
		reason := "Unfinished work skipped when campaign was closed."
		a.Data.Result = "SKIPPED"
		if a.Data.Answers == nil {
			a.Data.Answers = map[string]json.RawMessage{}
		}
		for _, b := range scenario.Procedure.Blocks {
			if b.Check != nil && len(a.Data.Answers[b.Check.ID]) == 0 {
				a.Data.Answers[b.Check.ID], err = json.Marshal(map[string]string{"outcome": "NOT_EXECUTED", "reason": reason})
				if err != nil {
					return err
				}
			}
		}
		if a.Data.Notes != "" {
			a.Data.Notes += "\n\n"
		}
		a.Data.Notes += reason
		if err := campaign.ValidateDraft(scenario.Procedure, a.Data); err != nil {
			return &FinalizationError{err.Error()}
		}
		raw, err := json.Marshal(a.Data)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE campaign_attempt SET data=$2,updated_by=$3 WHERE id=$1`, a.ID, raw, actor); err != nil {
			return err
		}
		a.UpdatedBy = &actor
		if _, err := finalizeAttemptTx(ctx, tx, a, p, scenario, p.Definition.Manifest.Scenarios[i].Path, now, actor,
			map[string]any{"reason": reason, "closed_by": actor, "closed_at": now, "campaign_revision": e.Revision + 1}); err != nil {
			return err
		}
	}
	return nil
}
