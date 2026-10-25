package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nesono/evidence-store/internal/blob"
	"github.com/nesono/evidence-store/internal/campaign"
	"github.com/nesono/evidence-store/internal/model"
	"github.com/nesono/evidence-store/internal/validate"
)

// FinalizationError is user-correctable; storage failures stay server errors.
type FinalizationError struct{ Message string }

func (e *FinalizationError) Error() string { return e.Message }

// FinalizeAttempt serializes against saving the same row, then creates evidence
// and marks the attempt immutable in the very same transaction. A retry of the
// accepted saved revision/time returns the existing link, including after expiry.
func (s *CampaignStore) FinalizeAttempt(ctx context.Context, id uuid.UUID, owner string, revision int64, finished time.Time) (*CampaignAttempt, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, campaignSweepLock); err != nil {
		return nil, false, err
	}
	a, err := scanAttempt(tx.QueryRow(ctx, `SELECT `+attemptColumns+` FROM campaign_attempt WHERE id=$1 AND owner=$2 FOR UPDATE`, id, owner))
	if err != nil {
		return nil, false, err
	}
	if a.FinalizedAt != nil {
		if a.FinalizedRevision == nil || *a.FinalizedRevision != revision || a.FinishedAt == nil || !a.FinishedAt.Equal(finished) {
			return nil, false, ErrAttemptConflict
		}
		return a, false, tx.Commit(ctx)
	}
	if a.Revision != revision {
		return nil, false, ErrAttemptConflict
	}
	p, err := scanCampaign(tx.QueryRow(ctx, `SELECT `+campaignColumns+` FROM campaign_package WHERE id=$1`, a.CampaignID))
	if err != nil {
		return nil, false, err
	}
	scenario, path, err := finalScenario(p, a.ProcedureRef)
	if err != nil {
		return nil, false, err
	}
	if finished.IsZero() {
		return nil, false, &FinalizationError{"finished_at is required"}
	}
	if err := campaign.ValidateFinalization(scenario.Procedure, a.Data); err != nil {
		return nil, false, &FinalizationError{err.Error()}
	}
	evidence, err := attemptEvidence(a, p, scenario, path, finished)
	if err != nil {
		return nil, false, err
	}
	if errs := validate.EvidenceCreate(evidence); len(errs) > 0 {
		return nil, false, &FinalizationError{strings.Join(errs, "; ")}
	}
	result, err := insertOne(ctx, tx, evidence)
	if err != nil {
		return nil, false, err
	}
	if err := finalAssetRefs(ctx, tx, result.Evidence.ID, scenario.Assets); err != nil {
		return nil, false, err
	}
	// Evidence now owns log attachments. Releasing the draft roots in the same
	// transaction ensures evidence expiry can really release its media later.
	if _, err := tx.Exec(ctx, `DELETE FROM campaign_draft_blob_ref WHERE attempt_id=$1`, a.ID); err != nil {
		return nil, false, err
	}
	a, err = scanAttempt(tx.QueryRow(ctx, `UPDATE campaign_attempt SET finalized_at=now(), finished_at=$2,
		finalized_revision=revision, revision=revision+1, updated_at=now(), evidence_id=$3 WHERE id=$1 RETURNING `+attemptColumns, a.ID, finished, result.Evidence.ID))
	if err != nil {
		return nil, false, err
	}
	return a, true, tx.Commit(ctx)
}

func finalScenario(p *CampaignPackage, ref string) (campaign.PackagedScenario, string, error) {
	for i, scenario := range p.Definition.Scenarios {
		if scenario.Definition.ProcedureRef == ref {
			return scenario, p.Definition.Manifest.Scenarios[i].Path, nil
		}
	}
	return campaign.PackagedScenario{}, "", fmt.Errorf("stored attempt refers to missing procedure")
}

func attemptEvidence(a *CampaignAttempt, p *CampaignPackage, scenario campaign.PackagedScenario, path string, finished time.Time) (*model.EvidenceCreate, error) {
	metadata := make(map[string]any)
	for key, value := range scenario.Definition.Defaults.Metadata {
		metadata[key] = value
	}
	metadata["tags"] = scenario.Definition.Defaults.Tags
	if a.Data.Metadata != nil {
		// The saved form is the complete effective metadata (including deliberate
		// deletions of defaults), not a patch to reapply to package defaults.
		metadata = make(map[string]any)
		for key, value := range a.Data.Metadata {
			metadata[key] = value
		}
	}
	metadata["notes"] = a.Data.Notes
	metadata["procedure_modified"] = false
	metadata["campaign"] = map[string]any{
		"schema_version": 1, "campaign_id": a.CampaignID, "package_digest": p.Digest,
		"campaign_key": p.Definition.Manifest.Campaign.ID, "attempt_id": a.ID,
		"procedure_ref": a.ProcedureRef, "source": p.Definition.Manifest.Source,
		"scenario_path": path, "procedure": scenario, "answers": a.Data.Answers,
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	// No client submission token is needed: the locked attempt is the unique
	// finalization identity. Each rerun is a fresh attempt and evidence record.
	typeName, source := a.Data.EvidenceType, a.Data.Source
	if typeName == "" {
		typeName = scenario.Definition.Defaults.EvidenceType
	}
	if source == "" {
		source = a.Owner
	}
	return &model.EvidenceCreate{Repo: a.Data.Repo, Branch: a.Data.Branch, RCSRef: a.Data.RCSRef,
		ProcedureRef: a.ProcedureRef, EvidenceType: typeName,
		Source: source, Result: model.EvidenceResult(a.Data.Result), FinishedAt: model.FlexibleTime{Time: finished}, Metadata: encoded}, nil
}

func finalAssetRefs(ctx context.Context, tx pgx.Tx, evidenceID uuid.UUID, assets map[string]string) error {
	var refs []blob.Ref
	for _, value := range assets {
		digest, err := blob.ParseDigest(value)
		if err != nil {
			return err
		}
		refs = append(refs, blob.Ref{Digest: digest})
	}
	return recordBlobRefs(ctx, tx, evidenceID, refs)
}
