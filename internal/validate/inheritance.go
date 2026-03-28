package validate

import "github.com/nesono/evidence-store/internal/model"

func InheritanceCreate(c *model.InheritanceCreate) []string {
	var errs []string

	if c.Repo == "" {
		errs = append(errs, "repo is required")
	}
	if c.SourceRCSRef == "" {
		errs = append(errs, "source_rcs_ref is required")
	}
	if c.TargetRCSRef == "" {
		errs = append(errs, "target_rcs_ref is required")
	}
	if c.Justification == "" {
		errs = append(errs, "justification is required")
	}
	if c.CreatedBy == "" {
		errs = append(errs, "created_by is required")
	}

	return errs
}
