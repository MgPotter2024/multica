ALTER TABLE runtime_profile DROP CONSTRAINT IF EXISTS runtime_profile_protocol_family_check;

-- Add ZCode (`zcode`) to the built-in runtime families. The daemon drives
-- ZCode's official app-server through zcode-acp-server's ACP translation.
-- NOT VALID preserves the historical tolerance used by prior whitelist
-- migrations for old rows.
ALTER TABLE runtime_profile ADD CONSTRAINT runtime_profile_protocol_family_check
    CHECK (protocol_family IN (
        'claude',
        'codebuddy',
        'codex',
        'copilot',
        'opencode',
        'openclaw',
        'hermes',
        'pi',
        'cursor',
        'kimi',
        'kiro',
        'antigravity',
        'qoder',
        'traecli',
        'deveco',
        'grok',
        'zcode'
    )) NOT VALID;
