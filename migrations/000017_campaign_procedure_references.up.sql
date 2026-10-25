-- Change only the parsed projection. Original CAS bytes, digest, import UUID,
-- and manifest format_version continue to describe the imported archive.
UPDATE campaign_package AS p
SET definition = jsonb_set(
    jsonb_set(p.definition, '{manifest,scenarios}', (
        SELECT jsonb_agg((entry - 'id') || jsonb_build_object(
            'procedure_ref', p.definition->'scenarios'->(position::int - 1)->'definition'->'procedure_ref'
        ) ORDER BY position)
        FROM jsonb_array_elements(p.definition->'manifest'->'scenarios') WITH ORDINALITY AS entries(entry, position)
    )),
    '{scenarios}', (
        SELECT jsonb_agg(jsonb_set(scenario, '{definition}', (scenario->'definition') - 'id') ORDER BY position)
        FROM jsonb_array_elements(p.definition->'scenarios') WITH ORDINALITY AS scenarios(scenario, position)
    )
);
