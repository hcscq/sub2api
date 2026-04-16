-- Add claude-opus-4-7 aliases to persisted Antigravity and Bedrock model_mapping.
--
-- Strategy:
-- - Only append the missing key
-- - Preserve any existing custom model_mapping values

UPDATE accounts
SET credentials = jsonb_set(
    COALESCE(credentials, '{}'::jsonb),
    '{model_mapping,claude-opus-4-7}',
    '"claude-opus-4-7"'::jsonb,
    true
)
WHERE platform = 'antigravity'
  AND deleted_at IS NULL
  AND credentials->'model_mapping' IS NOT NULL
  AND credentials->'model_mapping'->>'claude-opus-4-7' IS NULL;

UPDATE accounts
SET credentials = jsonb_set(
    COALESCE(credentials, '{}'::jsonb),
    '{model_mapping,claude-opus-4-7}',
    '"us.anthropic.claude-opus-4-7-v1"'::jsonb,
    true
)
WHERE platform = 'anthropic'
  AND type = 'bedrock'
  AND deleted_at IS NULL
  AND credentials->'model_mapping' IS NOT NULL
  AND credentials->'model_mapping'->>'claude-opus-4-7' IS NULL;
