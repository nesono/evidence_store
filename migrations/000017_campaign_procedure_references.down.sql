-- The projection's reference keys are also unique legacy IDs for the old UI's
-- joins. Exact legacy IDs remain available in the unchanged original archive.
UPDATE campaign_package AS p
SET definition = jsonb_set(
    jsonb_set(p.definition, '{manifest,scenarios}', (
        SELECT jsonb_agg((entry - 'procedure_ref') || jsonb_build_object('id', entry->'procedure_ref') ORDER BY position)
        FROM jsonb_array_elements(p.definition->'manifest'->'scenarios') WITH ORDINALITY AS entries(entry, position)
    )),
    '{scenarios}', (
        SELECT jsonb_agg(jsonb_set(scenario, '{definition}',
            (scenario->'definition') || jsonb_build_object('id', scenario->'definition'->'procedure_ref')
        ) ORDER BY position)
        FROM jsonb_array_elements(p.definition->'scenarios') WITH ORDINALITY AS scenarios(scenario, position)
    )
);
