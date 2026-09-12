-- =============================================================================
-- RULE ENGINE
-- =============================================================================
-- The Rule Engine is responsible for processing operational events such as
-- Alerts and Incidents, constructing the execution context required for
-- rule evaluation, and executing predefined RCA rules.
-- optional idempotent response caching keyed by request_id
--
--  When an event is received, the engine:
--   1. Identifies the target managed object(s).
--   2. Collects contextual information using Context Profiles, including
--      Vdu, Vnfc, configuration and other relevant system data.
--   3. Builds a context snapshot for rule execution.
--   4. Run all rule to identify cause and recommend corrective actions.
-- =============================================================================

-- -----------------------------------------------------------------------------
-- context_profile
-- Defines which alert inputs the profile applies to and which context
-- providers must run. The complete profile is stored in one row.
-- -----------------------------------------------------------------------------
CREATE TABLE context_profile (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    name        VARCHAR     NOT NULL UNIQUE,
    -- Stable identifier used by the Context Builder to report which profiles
    -- matched a request. UNIQUE so a matched-profile name means one profile.
    description TEXT,

    selector    JSONB       NOT NULL DEFAULT '{}'::jsonb,
    -- Alert-matching predicate used to select this profile.
    -- Example:
    -- {
    --   "probable_causes": [],
    --   "alert_types": [],
    --   "source_paths": ["ims.vdu_sb_logic"],
    --   "additional_information": {"<key>": ["<value>"]}
    -- }
    -- source_paths is optional; entries must have exactly two ltree labels
    -- (<namespace>.<vdu>). Each matches that VDU and its descendants,
    -- case-insensitively. Values are ORed; selector fields are ANDed per alert.

    providers   JSONB       NOT NULL DEFAULT '{}'::jsonb,
    -- Context-provider configuration.
    -- Example:
    -- {
    --   "vdu": [
    --     "ims.vdu_sb_sip_core",
    --     "ims.vdu_cs_loadbalancer_icscf",
    --     "ims.vdu_cs_sip_icscf",
    --     "ims.vdu_cs_logic"
    --   ],
    --   // A VDU provider result includes the VDU and all of its VNFCs.
    --   // The configuration provider GETs <base>/<path>.<key> (where <base>
    --   // comes from RE_CONFIGURATION_BASE_URL) and reads currentValue.
    --   // Profiles declare keys. Targets keep the matching alert's full
    --   // source_path; the provider uses its first two labels in the URL.
    --   "configuration": [
    --     "log_file_count", "log_file_size", "log_level", "limit_memory"
    --   ]
    -- }

    enabled     BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_context_profile_enabled ON context_profile (enabled);
CREATE INDEX idx_context_profile_selector ON context_profile USING GIN (selector);

-- -----------------------------------------------------------------------------
-- rca_rule
-- Each rule represents a scenario predefined by the operator.
-- The rule body is stored directly on the rule; updates replace the
-- current definition in place.
-- -----------------------------------------------------------------------------
CREATE TABLE rca_rule (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    name         VARCHAR     NOT NULL UNIQUE,
    description  TEXT,

    rule_content TEXT        NOT NULL,
    -- RCA rule source/content interpreted by the analysis engine.

    salience     INT         NOT NULL DEFAULT 0,
    -- Higher values may be evaluated first when rule ordering is required.

    enabled      BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_rca_rule_enabled_salience
    ON rca_rule (enabled, salience DESC);
