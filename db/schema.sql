-- =============================================================================
-- AIOps System — Complete Database Schema
-- Standards: 3GPP TS 28.111 (FM), TS 28.532 (Perf Threshold),
--            TS 28.623 (NRM), TS 28.567 (CCL), TS 28.104 (MDA)
--
-- Managed Object tree (ltree, path as PK):
--   ims.vdu_<name>                              VDU
--   ims.vdu_<name>.metric_<name>               performance_metric
--   ims.vdu_<name>.metric_<name>.tr_<uuid8>    threshold_rule
--   ims.vdu_<name>.vnfc_<name>                 VNFC
--   ims.node_<name>                            NODE
--   ims.ccl_<name>                             CCL instance
--
-- Non-tree tables (event / correlation / execution layer):
--   alert, incident, incident_alert, rca_result,
--   ccl_scope, ccl_report,
--   ccl_threshold_adjustment_report, ccl_fault_management_report,
--   recommendation, activation_job, fallback_descriptor
-- =============================================================================

CREATE EXTENSION IF NOT EXISTS "pgcrypto";
CREATE EXTENSION IF NOT EXISTS "btree_gin";
CREATE EXTENSION IF NOT EXISTS "ltree";


-- =============================================================================
-- LAYER 0 — MANAGED OBJECT BASE
-- =============================================================================

-- -----------------------------------------------------------------------------
-- managed_object
-- Stable topology registry. Every addressable resource in the system
-- (VDU, VNFC, performance_metric, threshold_rule, NODE, CCL) has exactly
-- one row here. Event/output tables (alert, incident, ccl_report, etc.)
-- are NOT in this table — they reference it via FK.
--
-- path is the PK and the FK target for all child tables and event tables.
-- parent_path provides referential integrity for the tree structure;
-- it is always subpath(path, 0, nlevel(path) - 1).
--
-- ltree label convention:
--   VDU           ims.vdu_<vdu_name>
--   metric        ims.vdu_<vdu_name>.metric_<metric_name>
--   threshold     ims.vdu_<vdu_name>.metric_<metric_name>.tr_<uuid_first8>
--   VNFC          ims.vdu_<vdu_name>.vnfc_<pod_name>
--   NODE          ims.node_<hostname> / todo
--   CCL           ims.ccl_<ccl_name>
--   Label rules: lowercase, non-alphanumeric chars → underscore.
-- -----------------------------------------------------------------------------
CREATE TABLE managed_object (
    path        LTREE       PRIMARY KEY,

    mo_class    VARCHAR     NOT NULL,
    -- Enum: VDU | VNFC | PERFORMANCE_METRIC | THRESHOLD_RULE | NODE | CCL

    parent_path LTREE       REFERENCES managed_object (path) ON DELETE RESTRICT,
    -- NULL for root-level objects (VDU, NODE, CCL).
    -- Tree structure:
    --   VDU             → NULL
    --   VNFC            → parent is VDU
    --   PERFORMANCE_METRIC → parent is VDU
    --   THRESHOLD_RULE  → parent is PERFORMANCE_METRIC
    --   NODE            → NULL
    --   CCL             → NULL

    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- GIST: powers subtree operators (<@, @>, ~, ?)
CREATE INDEX idx_mo_path_gist ON managed_object USING GIST (path);
-- B-tree: fast exact-match on path (PK lookups, FK joins)
CREATE INDEX idx_mo_path_btree ON managed_object USING BTREE (path);
-- Adjacency: fast single-level child listing via parent_path
CREATE INDEX idx_mo_parent ON managed_object (parent_path);
-- Class filter: WHERE mo_class = 'THRESHOLD_RULE'
CREATE INDEX idx_mo_class  ON managed_object (mo_class);


-- =============================================================================
-- LAYER 1 — TOPOLOGY
-- =============================================================================

-- -----------------------------------------------------------------------------
-- vdu
-- NF type blueprint. Owns performance_metric and threshold_rule definitions
-- since thresholds apply uniformly to all running VNFC instances of this type.
-- Path: ims.vdu_<name>   Parent: NULL
-- -----------------------------------------------------------------------------
CREATE TABLE vdu (
    path        LTREE       PRIMARY KEY REFERENCES managed_object (path) ON DELETE CASCADE,
    -- ltree path shared with managed_object. Not generated here.
    -- Example: ims.vdu_sblg

    name        TEXT        NOT NULL UNIQUE,
    -- VDU type name. Used as the path label segment.
    -- Example: 'sblg' | 'sbsipc' | 'cssipi'

    type        TEXT        NOT NULL,
    -- VDU functional classification.
    -- Example: LOGIC | SIPGW | DIAGW

    namespace   TEXT        NOT NULL,
    -- Kubernetes namespace this VDU is deployed in.

    workload    TEXT        NOT NULL,
    -- Kubernetes workload type managing VNFC pods.
    -- Enum: Deployment | StatefulSet

    replicas    INTEGER     NOT NULL,
    -- Desired number of running VNFC instances.

    selector TEXT,
    -- Optional Kubernetes label selector override
    -- NULL -> PRF derives 'app=' || name at query time.

    nf_config   JSONB,
    -- NF-specific configuration blob shared across all VNFCs of this type.
    -- Example: { "sip_port": 5060, "media_ports": [10000, 20000] }

    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);


-- -----------------------------------------------------------------------------
-- vnfc
-- A single running NF instance (Kubernetes pod). Child of its VDU in the tree.
-- DN is stable across pod restarts — only k8s_uid and status change.
-- Path: ims.vdu_<name>.vnfc_<pod_name>   Parent: vdu
-- -----------------------------------------------------------------------------
CREATE TABLE vnfc (
    path            LTREE       PRIMARY KEY REFERENCES managed_object (path) ON DELETE CASCADE,
    -- Example: ims.vdu_sblg.vnfc_sblg_0

    -- node_path       LTREE       REFERENCES managed_object (path),
    -- -- Current host NODE path. Updated on pod reschedule.
    -- -- NOT a tree parent — VNFC containment is under VDU, not NODE.
    -- -- Example: ims.node_worker_01

    k8s_uid         TEXT,
    -- Kubernetes metadata.uid of the current pod instance.
    -- Updated on pod recreation (new UID, same path).
    -- NULL if pod is currently down or pending.

    name            TEXT        NOT NULL,
    -- Kubernetes pod name. Stable for StatefulSet pods.
    -- Example: 'sblg-0' | 'sipc-1'

    status          TEXT        NOT NULL,
    -- Current pod lifecycle status.
    -- Enum: RUNNING | TERMINATED | UNKNOWN

    instance_config JSONB,
    -- Instance-level config overrides applied on top of vdu.nf_config.
    -- Example: { "ip": "10.0.0.5", "zone": "zone-a" }

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_vnfc_status    ON vnfc (status);
-- CREATE INDEX idx_vnfc_node_path ON vnfc (node_path);

-- =============================================================================
-- vnfc_binding
-- Pod <-> slot mapping. PRF is sole writer
-- =============================================================================

CREATE TABLE vnfc_binding (
    id          BIGSERIAL   PRIMARY KEY,
    -- Monotonic row id; MAX(id) per vnfc_path gives the current state.

    vnfc_path   LTREE       NOT NULL REFERENCES vnfc(path),
    -- Stable slot label, e.g. ims.vdu_sblg.vnfc_sblg_2
    -- Multiple rows per slot over time (append-only history)

    vdu_path    LTREE       NOT NULL,
    -- Parent VDU, e.g. ims.vdu_sblg. Scopes slot queries
    
    pod_name    VARCHAR,
    -- Pod occupying this slot at the time of this row. NULL on seed row.

    pod_ready   BOOLEAN     NOT NULL DEFAULT false,
    -- true = pod is Ready and this row is the active binding.

    bound_at    TIMESTAMPTZ,
    -- When this pod was bound. NULL on seed/unbind rows.

    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- At most one actively-ready binding per pod per VDU
CREATE UNIQUE INDEX uq_vnfc_binding_pod
    ON vnfc_binding (vdu_path, pod_name)
    WHERE pod_ready = true;

-- Past current-row lookup per slot (latest id per vnfc_path).
CREATE INDEX idx_vnfc_binding_slot ON vnfc_binding (vnfc_path, id DESC);

-- Fast free-slot scan per VDU
CREATE INDEX ix_vnfc_binding_free
    ON vnfc_binding (vdu_path, vnfc_path)
    WHERE NOT pod_ready;

-- -----------------------------------------------------------------------------
-- link
-- Directed network link between two VNFC instances.
-- Not an MO — it is a relationship, not an addressable resource.
-- Used by the RCA Module for topology-based propagation path traversal.
-- -----------------------------------------------------------------------------
CREATE TABLE link (
    src_path    LTREE       NOT NULL REFERENCES vnfc (path) ON DELETE CASCADE,
    -- Source VNFC path.

    dst_path    LTREE       NOT NULL REFERENCES vnfc (path) ON DELETE CASCADE,
    -- Destination VNFC path.

    src_ip      TEXT,
    src_port    INTEGER,
    dst_ip      TEXT,
    dst_port    INTEGER,

    protocol    TEXT,
    -- Protocol carried on this link.
    -- Enum: SIP | DIAMETER | H248 | HTTP2 | ISUP | MEGACO | RTP

    status      TEXT,
    -- Current link status.
    -- Enum: UP | DOWN | DEGRADED | UNKNOWN

    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (src_path, dst_path)
);

CREATE INDEX idx_link_dst ON link (dst_path);


-- =============================================================================
-- LAYER 2 — METRIC & THRESHOLD CONFIGURATION
-- =============================================================================

-- -----------------------------------------------------------------------------
-- performance_metric
-- A metric definition owned by a VDU. Represents one VictoriaMetrics series
-- family scoped to this VDU type. All VNFC instances of the VDU emit this
-- metric. Threshold rules hang off this node in the tree.
-- Path: ims.vdu_<name>.metric_<metric_name>   Parent: vdu
-- Reference: TS 28.550 MeasurementType
-- -----------------------------------------------------------------------------
CREATE TABLE performance_metric (
    path            LTREE       PRIMARY KEY REFERENCES managed_object (path) ON DELETE CASCADE,
    -- Example: ims.vdu_sblg.metric_cpu_utilization

    name            TEXT        NOT NULL UNIQUE,
    -- VictoriaMetrics metric name. Used as the path label segment.
    -- Example: 'cpu_utilization' | 'session_count' | 'packet_loss_rate'

    promql          TEXT        NOT NULL,

    unit            TEXT        NOT NULL
    -- Unit of measurement.
    -- Example: 'percent' | 'sessions' | 'packets_per_second'
);


-- -----------------------------------------------------------------------------
-- threshold_rule
-- A threshold definition owned by a performance_metric. PMF evaluates this
-- rule against VictoriaMetrics and raises an alert on crossing.
-- NF YANG datastore is authoritative when netconf_path is set;
-- this table is a mirror kept in sync by PMF.
-- UC2: threshold_value is adjusted dynamically by CCL via Rundeck + NETCONF.
-- Path: ims.vdu_<name>.metric_<name>.tr_<uuid_first8>   Parent: performance_metric
-- Reference: TS 28.532 ThresholdMonitor + ThresholdInfo
-- -----------------------------------------------------------------------------
CREATE TABLE threshold_rule (
    path                 LTREE       PRIMARY KEY REFERENCES managed_object (path) ON DELETE CASCADE,
    -- Example: ims.vdu_sblg.metric_cpu_utilization.tr_3f2a1b9c

    threshold_direction  VARCHAR     NOT NULL,
    -- Which crossing direction triggers an alert.
    -- Enum: UP | DOWN | UP_AND_DOWN

    threshold_value      NUMERIC     NOT NULL,
    -- Value the metric is compared against.
    -- Mutable: UC2 CCL adjusts this via Rundeck + NETCONF.

    hysteresis           NUMERIC     NOT NULL DEFAULT 0,
    -- Dead-band around threshold to prevent rapid re-triggering.
    -- Alert fires only when metric moves hysteresis beyond threshold.

    granularity_period   INTEGER     NOT NULL,
    -- PMF evaluation interval in seconds.
    -- Example: 60 = evaluate every minute.

    netconf_path         VARCHAR,
    -- YANG path in the NF datastore for this threshold attribute.
    -- NULL if threshold is PMF-only (not mirrored to NF).
    -- Example: /sbc:config/thresholds/cpu-utilization/value

    sync_status          VARCHAR,
    -- Mirror sync state. NULL if netconf_path is NULL.
    -- Enum: IN_SYNC | OUT_OF_SYNC
    --   IN_SYNC     — DB value matches NF YANG datastore
    --   OUT_OF_SYNC — NF changed independently; PMF sync pending

    last_synced_at       TIMESTAMPTZ,
    -- When PMF last confirmed NF value matches threshold_value.
    -- NULL if never synced or netconf_path is NULL.

    administrative_state VARCHAR     NOT NULL DEFAULT 'UNLOCKED',
    -- Enum: LOCKED | UNLOCKED

    operational_state    VARCHAR     NOT NULL DEFAULT 'ENABLED',
    -- Enum: ENABLED | DISABLED

    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_threshold_rule_admin ON threshold_rule (administrative_state);
CREATE INDEX idx_threshold_rule_ops   ON threshold_rule (operational_state);


-- =============================================================================
-- LAYER 3 — ALERTS
-- =============================================================================

-- -----------------------------------------------------------------------------
-- alert
-- An alert condition raised by a managed object.
-- source_path references the MO that raised the alert (VNFC, VDU, or NODE).
-- Not in the managed_object tree — alerts are high-volume ephemeral events,
-- not stable topology resources.
-- Subtree alert queries use: JOIN managed_object ON dn = source_path WHERE path <@ $subtree
-- Reference: 3GPP TS 28.111, ITU-T X.733
-- -----------------------------------------------------------------------------
CREATE TABLE alert (
    id                     UUID        PRIMARY KEY DEFAULT gen_random_uuid(),

    source_path            LTREE       NOT NULL REFERENCES managed_object (path),
    -- Path of the MO that raised this alert.
    -- Can point to VNFC, VDU, or NODE depending on alert origin.
    -- Example: ims.vdu_sblg.vnfc_sblg_0   (VNFC-level alert)
    --          ims.vdu_sblg                (VDU-level threshold crossing)
    --          ims.node_worker_01          (node resource exhaustion)

    alert_type             VARCHAR,
    -- TS 28.111 alert type classification.
    -- Enum: COMMUNICATIONS_ALERT | QUALITY_OF_SERVICE_ALERT |
    --       PROCESSING_ERROR_ALERT | EQUIPMENT_ALERT | ENVIRONMENTAL_ALERT

    probable_cause         VARCHAR,
    -- Per TS 28.111 Annex B / ITU-T X.733.
    -- Example: LINK_DOWN | COMMUNICATIONS_SUBSYSTEM_FAILURE |
    --          DEGRADED_SIGNAL | SOFTWARE_ERROR | CONGESTION |
    --          THRESHOLD_CROSSING

    perceived_severity     VARCHAR     NOT NULL,
    -- Enum: CRITICAL | MAJOR | MINOR | WARNING | INDETERMINATE | CLEARED

    state                  VARCHAR     NOT NULL DEFAULT 'ACTIVE',
    -- Enum: ACTIVE | ACKNOWLEDGED | CLEARED

    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),

    updated_at             TIMESTAMPTZ,
    -- When state or severity last changed.

    closed_at              TIMESTAMPTZ,
    -- NULL while alert is still active.

    additional_information JSONB
    -- TS 28.111 additionalInformation: arbitrary name-value pairs.
    -- Example: { "threshold_value": 80, "observed_value": 94.2 }
);

CREATE INDEX idx_alert_source   ON alert (source_path);
CREATE INDEX idx_alert_state    ON alert (state);
CREATE INDEX idx_alert_severity ON alert (perceived_severity);
CREATE INDEX idx_alert_raised   ON alert (created_at DESC);


-- =============================================================================
-- LAYER 4 — INCIDENT CORRELATION
-- =============================================================================

-- -----------------------------------------------------------------------------
-- incident
-- Operational container grouping related alerts for operator triage.
-- Not in the managed_object tree — incidents are correlation artifacts,
-- not topology resources.
-- Denormalized counters (worst_severity, active_alert_count, total_alert_count)
-- are maintained by the correlation engine on every member change to avoid
-- expensive aggregation on dashboard refresh.
-- -----------------------------------------------------------------------------
CREATE TABLE incident (
    id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),

    name               VARCHAR     NOT NULL,
    -- Human-readable incident label.
    -- Example: 'SBC-01 Link Cascade'

    status             VARCHAR     NOT NULL DEFAULT 'OPEN',
    -- Enum:
    --   OPEN          — active, member alerts still firing
    --   INVESTIGATING — operator has taken ownership
    --   CLOSED        — all member alerts cleared or resolved
    --   SUPPRESSED    — muted during maintenance window

    method             VARCHAR     NOT NULL,
    -- How this incident was formed.
    -- Enum: RULE_BASED | ML_BASED | MANUAL

    worst_severity     VARCHAR     NOT NULL DEFAULT 'INDETERMINATE',
    -- Denormalized: worst perceived_severity across all member alerts.
    -- Updated by correlation engine on every member add/change/remove.
    -- Enum: CRITICAL | MAJOR | MINOR | WARNING | INDETERMINATE

    active_alert_count INTEGER     NOT NULL DEFAULT 0,
    -- Denormalized: count of member alerts currently in ACTIVE state.

    total_alert_count  INTEGER     NOT NULL DEFAULT 0,
    -- Denormalized: total count of all member alerts including cleared.

    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at          TIMESTAMPTZ
    -- NULL while incident is still open.
);

CREATE INDEX idx_incident_status  ON incident (status);
CREATE INDEX idx_incident_updated ON incident (updated_at DESC);


-- -----------------------------------------------------------------------------
-- incident_alert
-- Junction between alert and incident.
-- One alert belongs to exactly one incident at any time (UNIQUE on alert_id).
-- -----------------------------------------------------------------------------
CREATE TABLE incident_alert (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),

    incident_id       UUID        NOT NULL REFERENCES incident (id) ON DELETE CASCADE,
    alert_id          UUID        NOT NULL UNIQUE REFERENCES alert (id) ON DELETE CASCADE,

    role              VARCHAR     NOT NULL,
    -- Structural role of this alert within the incident.
    -- Enum:
    --   TRIGGER  — first alert that caused incident to open (seed)
    --   SYMPTOM  — subsequent alert pulled in by correlation engine

    correlation_score FLOAT       NOT NULL DEFAULT 1.0
                      CHECK (correlation_score BETWEEN 0 AND 1),
    -- Confidence that this alert belongs to this incident. 0–1.
    -- Continuously updated by the correlation engine.
    -- Low scores may trigger eviction from the incident.

    added_at          TIMESTAMPTZ NOT NULL DEFAULT now()
    -- When the engine added this alert. Enables alert sequence reconstruction.
);

CREATE INDEX idx_incident_alert_incident ON incident_alert (incident_id);


-- -----------------------------------------------------------------------------
-- rca_result
-- One RCA conclusion per analysis cycle per incident.
-- Multiple rows per incident accumulate over time as the engine revises.
-- Only one ACTIVE result per incident at a time — enforced at application
-- layer by flipping previous result to SUPERSEDED before inserting new one.
-- -----------------------------------------------------------------------------
CREATE TABLE rca_result (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),

    incident_id      UUID        NOT NULL REFERENCES incident (id) ON DELETE CASCADE,

    root_alert_id    UUID        NOT NULL REFERENCES alert (id),
    -- Alert identified as the root cause. Must be a member of the incident
    -- via incident_alert (enforced at application layer).

    confidence_score FLOAT       NOT NULL CHECK (confidence_score BETWEEN 0 AND 1),

    method           VARCHAR     NOT NULL,
    -- Enum: RULE_BASED | ML_BASED | MANUAL

    description      TEXT,
    -- Plain-text reasoning chain shown to operators.
    -- Includes propagation path, correlated metrics, and confidence rationale.

    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_rca_incident ON rca_result (incident_id);



-- =============================================================================
-- LAYER 5 — MDA (TS 28.104)
-- =============================================================================
-- =============================================================================
-- mda_request
-- One row per POST /mda-requests call.
-- Tracks the full lifecycle of an analytics job.
-- =============================================================================
CREATE TABLE mda_request (
    id                      UUID        PRIMARY KEY DEFAULT gen_random_uuid(),

    status                  VARCHAR     NOT NULL DEFAULT 'ACCEPTED',
    -- ACCEPTED    — request validated and persisted, async worker not yet started
    -- RUNNING     — async worker started, pipeline in progress
    -- COMPLETED   — pipeline succeeded, callback delivered (consumer returned 204)
    -- FAILED      — pipeline error or callback failed after retries
    -- CANCELLED   — consumer called DELETE before or during execution

    mda_type                VARCHAR     NOT NULL,
    -- CORRELATION_ANALYTICS_ALERT_CORRELATION
    -- PREDICTIONS_PM_DATA

    reporting_target        TEXT,
    -- URI that MDAF POSTs the MDAReport callback to.
    -- NULL means no callback is delivered.
    -- Example: http://cclf.ifm-cclf.svc:8080/api/v1/mda-callback

    analytics_scope         JSONB       NOT NULL,
    -- analyticsScope.managedEntitiesScope from request body.
    -- Array of DN strings.
    -- Example: ["ims.vdu_sblg.vnfc_sblg_0", "ims.vdu_sblg.vnfc_sblg_1"]

    recommendations_scope   JSONB,
    -- recommendationsScope.managedEntitiesScope from request body.
    -- Only set for PREDICTIONS_PM_DATA requests.
    -- NULL for CORRELATION_ANALYTICS_ALERT_CORRELATION.
    -- Example: ["ims.vdu_sblg.metric_cpu.tr_3f2a1b9c"]

    requested_mda_outputs   JSONB,
    -- requestedMdaOutputs array from request body, stored verbatim.
    -- Used to filter which output IEs to include in the MDAReport callback.
    -- NULL means return all IEs for the given mda_type.

    error_reason            TEXT,
    -- Set on FAILED status. Human-readable description of what went wrong.
    -- NULL for all other statuses.

    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_mda_request_status   ON mda_request (status);
CREATE INDEX idx_mda_request_mda_type ON mda_request (mda_type);
CREATE INDEX idx_mda_request_created  ON mda_request (created_at DESC);


-- =============================================================================
-- mda_report
-- One row per MDAReport successfully delivered to the consumer.
-- Written after consumer returns 204 on callback.
-- Used by GET /mda-reports and GET /mda-reports/{reportId}.
-- =============================================================================
CREATE TABLE mda_report (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),

    mda_request_id  UUID        NOT NULL REFERENCES mda_request (id) ON DELETE CASCADE,
    -- The MDARequest that triggered this report.
    -- One mda_request produces at most one mda_report.

    mda_output      JSONB       NOT NULL,
    -- Full MDAOutput object as delivered in the callback, stored verbatim.
    -- Schema matches MDAOutput in MDAF.yaml:
    -- {
    --   "mdaType":          "PREDICTIONS_PM_DATA",
    --   "analyticsWindow":  { "startAt": ..., "stopAt": ... },
    --   "confidenceDegree": 0.87,
    --   "outputs": [ { ... } ]
    -- }
    -- Stored verbatim so GET /mda-reports can reconstruct the full response
    -- without re-running the pipeline.

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_mda_report_request_id ON mda_report (mda_request_id);
CREATE INDEX idx_mda_report_created    ON mda_report (created_at DESC);
-- Partial index for filtering by mda_type (extracted from mda_output):
CREATE INDEX idx_mda_report_mda_type
    ON mda_report ((mda_output->>'mdaType'));


-- =============================================================================
-- LAYER 6 — CCL (TS 28.567 ClosedControlLoop)
-- =============================================================================

-- -----------------------------------------------------------------------------
-- ccl_instance
-- Long-lived policy object representing a running ClosedControlLoop.
-- In the managed_object tree as a root-level MO (no topology parent).
-- Runs repeatedly on trigger_schedule or on external MDAF push.
-- Scope (what it monitors and controls) defined in ccl_scope.
-- Path: ims.ccl_<name>   Parent: NULL
-- Reference: TS 28.567 ClosedControlLoop IOC
-- -----------------------------------------------------------------------------
CREATE TABLE ccl_instance (
    path                 LTREE       PRIMARY KEY REFERENCES managed_object (path) ON DELETE CASCADE,
    -- Example: ims.ccl_threshold_optimizer_sbc

    name                 VARCHAR     NOT NULL UNIQUE,
    -- Normalized CCL name. Used as the path label segment.
    -- Example: 'threshold_optimizer_sbc'

    administrative_state VARCHAR     NOT NULL DEFAULT 'UNLOCKED',
    -- Enum: LOCKED | UNLOCKED

    operational_state    VARCHAR     NOT NULL DEFAULT 'ENABLED',
    -- Enum: ENABLED | DISABLED

    ccl_priority         INTEGER     NOT NULL DEFAULT 5
                         CHECK (ccl_priority BETWEEN 1 AND 10),
    -- Execution priority. 1 = lowest, 10 = highest.
    -- Used for conflict resolution when multiple CCLs target the same resource.

    ccl_type             VARCHAR     NOT NULL,
    -- Enum:
    --   NETWORK_PROBLEM_RECOVERY — UC2: dynamic threshold adjustment
    --   FAULT_MANAGEMENT         — UC1: fault remediation (future)

    desired_behavior     VARCHAR     NOT NULL DEFAULT 'NOTIFY_RECOMMENDATION',
    -- How the CCL acts on its decisions per TS 28.567.
    -- Enum:
    --   NOTIFY_RECOMMENDATION — produces recommendation, waits for operator approval
    --   DECISION_ACTIVATION   — executes autonomously, auto-approves recommendation
    --   DO_NOTHING            — analysis only, no action taken

    trigger_schedule     VARCHAR,
    -- Cron expression for periodic execution.
    -- NULL if triggered externally by MDAF push only.
    -- Example: '0 */6 * * *' = every 6 hours

    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ccl_type ON ccl_instance (ccl_type);
CREATE INDEX idx_ccl_ops  ON ccl_instance (operational_state);


-- -----------------------------------------------------------------------------
-- ccl_scope
-- Defines what a CCL instance monitors (MEASUREMENT) and what it may
-- act on (CONTROL). Two rows per CCL instance — one per scope_type.
--
-- object_parameters: specific objects within managed_entities in scope.
--   MEASUREMENT → performance_metric paths to feed MDAF.
--   CONTROL     → threshold_rule paths eligible for adjustment.
--
-- Reference: TS 28.567 CCLScope IOC
-- -----------------------------------------------------------------------------
CREATE TABLE ccl_scope (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),

    ccl_path          LTREE       NOT NULL REFERENCES ccl_instance (path) ON DELETE CASCADE,
    -- Which CCL instance this scope belongs to.

    scope_type        VARCHAR     NOT NULL,
    -- Enum: MEASUREMENT | CONTROL


    object_parameters JSONB,
    -- Specific objects that are in scope.
    -- MEASUREMENT example: ["ims.vdu_sblg.metric_cpu_utilization",
    --                        "ims.vdu_sblg.metric_session_count"]
    -- CONTROL example:     ["ims.vdu_sblg.metric_cpu_utilization.tr_3f2a1b9c",
    --                        "ims.vdu_sblg.metric_session_count.tr_9d1e4a2f"]

    UNIQUE (ccl_path, scope_type)
    -- One MEASUREMENT and one CONTROL scope per CCL instance.
);

CREATE INDEX idx_ccl_scope_path ON ccl_scope (ccl_path);


-- -----------------------------------------------------------------------------
-- ccl_report
-- Output container for one CCL analysis cycle.
-- SET NULL on ccl_path deletion so reports survive CCL removal per TS 28.567.
-- Type-specific detail in child tables (ccl_threshold_adjustment_report,
-- ccl_fault_management_report). Discriminated by report_type.
-- Reference: TS 28.567 CCLReport IOC
-- -----------------------------------------------------------------------------
CREATE TABLE ccl_report (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),

    ccl_path         LTREE       REFERENCES ccl_instance (path) ON DELETE SET NULL,
    -- Which CCL produced this report. NULL if CCL was deleted after report creation.

    report_type      VARCHAR     NOT NULL,
    -- Discriminator for the child detail table.
    -- Enum: THRESHOLD_ADJUSTMENT | FAULT_MANAGEMENT

    report_time      TIMESTAMPTZ NOT NULL,
    -- When the CCL completed this analysis cycle.

    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ccl_report_path ON ccl_report (ccl_path);
CREATE INDEX idx_ccl_report_time ON ccl_report (report_time DESC);


-- -----------------------------------------------------------------------------
-- ccl_threshold_adjustment_report
-- UC2 detail for ccl_report where report_type = THRESHOLD_ADJUSTMENT.
-- Holds PMDataOutput from TS 28.104 MDAF analysis. 1:1 with ccl_report.
-- Reference: TS 28.104 PMDataOutput, ThresholdAssessment, PmPrediction
-- -----------------------------------------------------------------------------
CREATE TABLE ccl_threshold_adjustment_report (
    id                     UUID        PRIMARY KEY DEFAULT gen_random_uuid(),

    ccl_report_id          UUID        NOT NULL UNIQUE
                           REFERENCES ccl_report (id) ON DELETE CASCADE,
    -- UNIQUE enforces 1:1 with ccl_report.

    pm_predictions         JSONB,
    -- Array of PmPrediction from TS 28.104 PMDataOutput.
    -- Each entry: { "pmName": str, "pmPredictedValue": num, "at": timestamp }
    -- Example: [
    --   { "pmName": "cpu_utilization", "pmPredictedValue": 91,
    --     "at": "2026-05-06T14:00:00Z" }
    -- ]

    threshold_assessments  JSONB,
    -- Array of ThresholdAssessment from TS 28.104.
    -- Identifies metrics where current threshold configuration is suboptimal.
    -- Each entry: { "performanceMetrics": [str], "timeWindow": {...},
    --               "confidenceScore": float }

    confidence_degree      FLOAT,
    -- Overall MDAF analytics confidence for this cycle. 0–1.

    analytics_window_start TIMESTAMPTZ,
    analytics_window_end   TIMESTAMPTZ
    -- Time window of PM data used for this analysis.
);


-- -----------------------------------------------------------------------------
-- ccl_fault_management_report
-- UC1 future: detail for ccl_report where report_type = FAULT_MANAGEMENT.
-- Placeholder — will hold GeneratedAlertResult array from TS 28.567.
-- Reference: TS 28.567 FaultManagementCCLReport, GeneratedAlertResult
-- -----------------------------------------------------------------------------
CREATE TABLE ccl_fault_management_report (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),

    ccl_report_id UUID        NOT NULL UNIQUE
                  REFERENCES ccl_report (id) ON DELETE CASCADE,

    incident_id   UUID        REFERENCES incident (id) ON DELETE SET NULL,
    -- Incident this fault management cycle analyzed.
    -- SET NULL on incident closure so report survives.

    report_time   TIMESTAMPTZ
);


CREATE TABLE activation_job (
    id               UUID    PRIMARY KEY DEFAULT gen_random_uuid(),

    type             VARCHAR NOT NULL DEFAULT 'FORWARD',
    -- FORWARD  — applying the recommendation's proposed_changes
    -- ROLLBACK — applying the inverse patch from fallback_descriptor

    mo_instance      LTREE   NOT NULL REFERENCES managed_object (path),
    -- DN of the managed object to act on.
    -- Example: ims.vdu_sblg.metric_cpu.tr_001

    attr_path        VARCHAR NOT NULL,
    -- Attribute path within the MO.
    -- Example: threshold_value

    op               VARCHAR NOT NULL,
    -- ADD | REMOVE | REPLACE

    value            JSONB,
    -- New value to apply. NULL for REMOVE.

    status           VARCHAR NOT NULL DEFAULT 'PENDING',
    -- PENDING | IN_PROGRESS | DONE | FAILED

    execution_result JSONB,
    -- Executor output. NULL until completed.
    -- Shape is executor-specific; not queried directly.

    executed_at      TIMESTAMPTZ,
    -- When executor started the job.

    completed_at     TIMESTAMPTZ,
    -- When executor reported completion.

    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_activation_job_status      ON activation_job (status);
CREATE INDEX idx_activation_job_type        ON activation_job (type);
CREATE INDEX idx_activation_job_mo_instance ON activation_job (mo_instance);


-- -----------------------------------------------------------------------------
-- fallback_descriptor
-- Pre-execution NF state capture and pre-computed inverse patch.
-- System-generated — created immediately before executor fires on a FORWARD job.
-- Never created by consumers directly (mirrors TS 28.572 §6.11).
-- 1:1 with activation_job; only FORWARD jobs get a fallback_descriptor.
--
-- Activating the fallback = inserting a new ROLLBACK activation_job using
-- the rollback_* columns verbatim. No transformation required.
-- -----------------------------------------------------------------------------
CREATE TABLE fallback_descriptor (
    id                    UUID    PRIMARY KEY DEFAULT gen_random_uuid(),

    activation_job_id     UUID    NOT NULL UNIQUE
                          REFERENCES activation_job (id) ON DELETE CASCADE,
    -- 1:1 with the FORWARD activation_job this descriptor was created for.

    before_value          JSONB   NOT NULL,
    -- Raw NF state read immediately before execution.
    -- { "mo_instance": ltree_path, "attr_path": string, "value": any }
    -- Ground truth for audit and manual recovery. Not queried directly.

    rollback_mo_instance  LTREE   NOT NULL,
    rollback_attr_path    VARCHAR NOT NULL,
    rollback_op           VARCHAR NOT NULL DEFAULT 'REPLACE',
    rollback_value        JSONB,
    -- Pre-computed inverse patch, exploded into columns matching activation_job.
    -- Always REPLACE; rollback_value taken from before_value at capture time.
    -- Copied as-is into a new ROLLBACK activation_job when fallback is activated:
    --
    --   INSERT INTO activation_job (type, mo_instance, attr_path, op, value)
    --   SELECT 'ROLLBACK', rollback_mo_instance, rollback_attr_path,
    --          rollback_op, rollback_value
    --   FROM fallback_descriptor
    --   WHERE activation_job_id = $1;

    fallback_status       VARCHAR NOT NULL DEFAULT 'NOT_NEEDED',
    -- NOT_NEEDED — forward job succeeded; no fallback triggered yet
    -- PENDING    — fallback activation_job created, awaiting completion
    -- DONE       — fallback activation_job reached DONE
    -- FAILED     — fallback activation_job reached FAILED; manual intervention required
    --
    -- API guard: 409 if fallback_status is already PENDING or DONE.
    -- Transitions: NOT_NEEDED → PENDING → DONE | FAILED

    activated_at          TIMESTAMPTZ,
    -- Set when fallback_status transitions to DONE or FAILED.

    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_fallback_status ON fallback_descriptor (fallback_status);

-- -----------------------------------------------------------------------------
-- recommendation
-- CCL-generated remediation suggestion produced from an MDAF report analysis.
-- proposed_changes holds the RecommendedActions slice read from the MDAF report
-- at initialization time — written once by CCL, never modified.
--
-- status tracks operator approval only.
-- Execution status is tracked on the linked activation_job.
--
-- When ccl_instance.desired_behavior = DECISION_ACTIVATION:
--   CCL sets status = APPROVED immediately on creation and creates the
--   activation_job without waiting for operator input.
-- When ccl_instance.desired_behavior = NOTIFY_RECOMMENDATION:
--   Operator approves or rejects via API. Approval creates the activation_job.
-- -----------------------------------------------------------------------------
CREATE TABLE recommendation (
    id                  UUID    PRIMARY KEY DEFAULT gen_random_uuid(),

    ccl_report_id       UUID    NOT NULL REFERENCES ccl_report (id) ON DELETE CASCADE,
    -- CCL report that produced this recommendation.
    -- ccl_instance implied through ccl_report.ccl_path.

    activation_job_id   UUID    REFERENCES activation_job (id) ON DELETE SET NULL,
    -- NULL until recommendation is approved.
    -- SET NULL if activation_job is deleted (preserves recommendation history).

    proposed_changes    JSONB,
    -- Semantic patch copied verbatim from the MDAF report at initialization.
    -- RecommendedActions structure per TS 28.104:
    -- { "moInstance": ltree_path, "path": attr_path,
    --   "op": "ADD | REMOVE | REPLACE", "value": any,
    --   "additionalText": [string] }
    -- Source of truth for API RecommendationDetail.proposedChanges.
    -- activation_job patch columns are derived from this at approval time.

    title               VARCHAR NOT NULL,
    -- Short label for operator UI.
    -- Example: 'Lower threshold_value on cpu metric — sblg VDU'

    description         TEXT,
    -- Full reasoning: current value, proposed value, predicted peak,
    -- MDAF confidence score. Written by CCL decision step.

    priority            VARCHAR NOT NULL DEFAULT 'MEDIUM',
    -- HIGH | MEDIUM | LOW

    status              VARCHAR NOT NULL DEFAULT 'PENDING',
    -- Operator approval status — not execution status.
    -- PENDING  — awaiting operator decision
    -- APPROVED — operator accepted, or CCL auto-approved (DECISION_ACTIVATION)
    -- REJECTED — operator dismissed
    --
    -- Derived API status (RecommendationSummary.status) combines this with
    -- activation_job.status:
    --   PENDING  + —           → PENDING
    --   APPROVED + —           → APPROVED
    --   REJECTED + —           → REJECTED
    --   APPROVED + IN_PROGRESS → EXECUTING
    --   APPROVED + DONE        → EXECUTED
    --   APPROVED + FAILED      → FAILED

    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_recommendation_report  ON recommendation (ccl_report_id);
CREATE INDEX idx_recommendation_status  ON recommendation (status);
CREATE INDEX idx_recommendation_created ON recommendation (created_at DESC);


-- =============================================================================
-- LAYER 7 — RULE ENGINE
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
    --   "additional_information": {"<key>": ["<value>"]}
    -- }

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
    --   "configuration": [
    --        {
    --              "path": "ims.vdu_sb_logic.vnfc_sb_logic_1",
    --              "key": "number_of_log_file",
    --              "url": "http://api/v1/ims.vdu_sb_logic.vnfc_sb_logic_1/num_of_log_file"
    --        }
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
