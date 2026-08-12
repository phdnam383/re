-- Unified test seed for rebuild/db/schema.sql.
-- Scenarios: LINK_TO_PEER_SIPGW_DOWN, LINK_TO_PEER_DIAGW_DOWN, TPS_OVERLOADED.
-- Effective configuration is not stored in this database. For TPS, the
-- CONFIGURATION provider must return number_of_log_file=5 for vnfc_sb_logic_1.

BEGIN;

-- ---------------------------------------------------------------------------
-- Topology and VNFC state
-- ---------------------------------------------------------------------------
CREATE TEMP TABLE seed_component (
    component_key  TEXT PRIMARY KEY,
    vdu_path       LTREE NOT NULL,
    vdu_name       TEXT NOT NULL,
    vdu_type       TEXT NOT NULL,
    namespace      TEXT NOT NULL,
    workload       TEXT NOT NULL,
    replicas       INTEGER NOT NULL,
    selector       TEXT,
    nf_config      JSONB,
    vnfc_path      LTREE NOT NULL UNIQUE,
    vnfc_name      TEXT NOT NULL,
    vnfc_status    TEXT NOT NULL,
    k8s_uid        TEXT,
    instance_config JSONB
) ON COMMIT DROP;

INSERT INTO seed_component VALUES
  ('sip_source_1', 'ims.vdu_sb_sip_core', 'sb_sip_core', 'SIPGW', 'dev-sb', 'StatefulSet', 2, 'app=sb-sip-core', '{"sip_port":5060,"transport":"TCP"}', 'ims.vdu_sb_sip_core.vnfc_sb_sip_core_1', 'vnfc_sb_sip_core_1', 'RUNNING', 'uid-sb-sip-core-1', '{"ip":"10.55.60.10","zone":"zone-a"}'),
  ('sip_source_2', 'ims.vdu_sb_sip_core', 'sb_sip_core', 'SIPGW', 'dev-sb', 'StatefulSet', 2, 'app=sb-sip-core', '{"sip_port":5060,"transport":"TCP"}', 'ims.vdu_sb_sip_core.vnfc_sb_sip_core_2', 'vnfc_sb_sip_core_2', 'RUNNING', 'uid-sb-sip-core-2', '{"ip":"10.55.60.11","zone":"zone-b"}'),
  ('sip_lb', 'ims.vdu_cs_loadbalancer_icscf', 'cs_loadbalancer_icscf', 'SIPGW', 'dev-sb', 'Deployment', 1, 'app=cs-lb-icscf', '{"listen_port":5060,"protocol":"SIP"}', 'ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1', 'vnfc_cs_loadbalancer_icscf_1', 'TERMINATED', NULL, '{"ip":"10.55.70.37","failure_reason":"SIP health check failed"}'),
  ('sip_icscf', 'ims.vdu_cs_sip_icscf', 'cs_sip_icscf', 'SIPGW', 'dev-sb', 'StatefulSet', 1, 'app=cs-sip-icscf', '{"sip_port":5060,"role":"I-CSCF"}', 'ims.vdu_cs_sip_icscf.vnfc_cs_sip_icscf_1', 'vnfc_cs_sip_icscf_1', 'TERMINATED', NULL, '{"ip":"10.55.70.38","failure_reason":"Pod not ready"}'),
  ('sip_logic', 'ims.vdu_cs_logic', 'cs_logic', 'LOGIC', 'dev-sb', 'StatefulSet', 1, 'app=cs-logic', '{"role":"CALL_CONTROL"}', 'ims.vdu_cs_logic.vnfc_cs_logic_1', 'vnfc_cs_logic_1', 'TERMINATED', NULL, '{"ip":"10.55.70.39","failure_reason":"Process exited"}'),

  ('dia_source_1', 'ims.vdu_sb_diameter_core', 'sb_diameter_core', 'DIAGW', 'dev-sb', 'StatefulSet', 2, 'app=sb-diameter-core', '{"application_protocol":"DIAMETER","transport":"SCTP"}', 'ims.vdu_sb_diameter_core.vnfc_sb_diameter_core_1', 'vnfc_sb_diameter_core_1', 'RUNNING', 'uid-sb-diameter-core-1', '{"ip":"10.56.60.10","port":3868,"zone":"zone-a"}'),
  ('dia_source_2', 'ims.vdu_sb_diameter_core', 'sb_diameter_core', 'DIAGW', 'dev-sb', 'StatefulSet', 2, 'app=sb-diameter-core', '{"application_protocol":"DIAMETER","transport":"SCTP"}', 'ims.vdu_sb_diameter_core.vnfc_sb_diameter_core_2', 'vnfc_sb_diameter_core_2', 'RUNNING', 'uid-sb-diameter-core-2', '{"ip":"10.56.60.11","port":3868,"zone":"zone-b"}'),
  ('dia_lb', 'ims.vdu_cs_loadbalancer_diagw', 'cs_loadbalancer_diagw', 'DIAGW', 'dev-sb', 'Deployment', 1, 'app=cs-lb-diagw', '{"application_protocol":"DIAMETER","transport":"SCTP"}', 'ims.vdu_cs_loadbalancer_diagw.vnfc_cs_loadbalancer_diagw_1', 'vnfc_cs_loadbalancer_diagw_1', 'TERMINATED', NULL, '{"ip":"10.56.70.37","port":3868,"failure_reason":"SCTP association health check failed"}'),
  ('dia_router', 'ims.vdu_cs_diameter_router', 'cs_diameter_router', 'DIAGW', 'dev-sb', 'StatefulSet', 1, 'app=cs-diameter-router', '{"application_protocol":"DIAMETER","transport":"SCTP"}', 'ims.vdu_cs_diameter_router.vnfc_cs_diameter_router_1', 'vnfc_cs_diameter_router_1', 'TERMINATED', NULL, '{"ip":"10.56.70.38","port":3868,"failure_reason":"Diameter routing process is not ready"}'),
  ('dia_logic', 'ims.vdu_cs_diag_logic', 'cs_diag_logic', 'LOGIC', 'dev-sb', 'StatefulSet', 1, 'app=cs-diag-logic', '{"application_protocol":"HTTP2","transport":"TCP"}', 'ims.vdu_cs_diag_logic.vnfc_cs_diag_logic_1', 'vnfc_cs_diag_logic_1', 'TERMINATED', NULL, '{"ip":"10.56.70.39","port":8080,"failure_reason":"Routing logic process exited"}'),
  ('dia_hss', 'ims.vdu_cs_hss_connector', 'cs_hss_connector', 'DIAGW', 'dev-sb', 'Deployment', 1, 'app=cs-hss-connector', '{"application_protocol":"DIAMETER","transport":"SCTP"}', 'ims.vdu_cs_hss_connector.vnfc_cs_hss_connector_1', 'vnfc_cs_hss_connector_1', 'RUNNING', 'uid-cs-hss-connector-1', '{"ip":"10.56.70.40","port":3868}'),

  ('tps_1', 'ims.vdu_sb_logic', 'sb_logic', 'LOGIC', 'ims', 'StatefulSet', 3, 'app=sb-logic', NULL, 'ims.vdu_sb_logic.vnfc_sb_logic_1', 'sb-logic-1', 'RUNNING', 'a1b2c3d4-0001-0000-0000-000000000001', NULL),
  ('tps_2', 'ims.vdu_sb_logic', 'sb_logic', 'LOGIC', 'ims', 'StatefulSet', 3, 'app=sb-logic', NULL, 'ims.vdu_sb_logic.vnfc_sb_logic_2', 'sb-logic-2', 'TERMINATED', NULL, NULL),
  ('tps_3', 'ims.vdu_sb_logic', 'sb_logic', 'LOGIC', 'ims', 'StatefulSet', 3, 'app=sb-logic', NULL, 'ims.vdu_sb_logic.vnfc_sb_logic_3', 'sb-logic-3', 'TERMINATED', NULL, NULL);

INSERT INTO managed_object (path, mo_class, parent_path, created_at, updated_at)
-- NULL::ltree, not a bare NULL. In an INSERT ... SELECT the target column type
-- does not reach the select list, so a bare NULL is resolved as text and
-- PostgreSQL refuses to assign text to an ltree column.
SELECT DISTINCT vdu_path, 'VDU', NULL::ltree, '2026-06-18T00:00:00Z'::timestamptz, '2026-06-18T00:00:00Z'::timestamptz
FROM seed_component
ON CONFLICT (path) DO UPDATE SET mo_class = EXCLUDED.mo_class, updated_at = EXCLUDED.updated_at;

INSERT INTO vdu (path, name, type, namespace, workload, replicas, selector, nf_config, updated_at)
SELECT DISTINCT ON (vdu_path) vdu_path, vdu_name, vdu_type, namespace, workload,
       replicas, selector, nf_config, '2026-06-18T00:00:00Z'
FROM seed_component ORDER BY vdu_path, component_key
ON CONFLICT (path) DO UPDATE SET
  name = EXCLUDED.name, type = EXCLUDED.type, namespace = EXCLUDED.namespace,
  workload = EXCLUDED.workload, replicas = EXCLUDED.replicas,
  selector = EXCLUDED.selector, nf_config = EXCLUDED.nf_config,
  updated_at = EXCLUDED.updated_at;

INSERT INTO managed_object (path, mo_class, parent_path, created_at, updated_at)
SELECT vnfc_path, 'VNFC', vdu_path, '2026-06-18T00:00:00Z', '2026-06-18T00:00:00Z'
FROM seed_component
ON CONFLICT (path) DO UPDATE SET
  mo_class = EXCLUDED.mo_class, parent_path = EXCLUDED.parent_path,
  updated_at = EXCLUDED.updated_at;

INSERT INTO vnfc (path, k8s_uid, name, status, instance_config, created_at, updated_at)
SELECT vnfc_path, k8s_uid, vnfc_name, vnfc_status, instance_config,
       '2026-06-18T00:00:00Z', '2026-06-18T00:00:00Z'
FROM seed_component
ON CONFLICT (path) DO UPDATE SET
  k8s_uid = EXCLUDED.k8s_uid, name = EXCLUDED.name, status = EXCLUDED.status,
  instance_config = EXCLUDED.instance_config, updated_at = EXCLUDED.updated_at;

-- Directed links. IP/port details remain on the VNFC instance_config; rules
-- need the endpoint paths, protocol and current status.
INSERT INTO link (src_path, dst_path, protocol, status, created_at, updated_at) VALUES
  ('ims.vdu_sb_sip_core.vnfc_sb_sip_core_1', 'ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1', 'SIP', 'DOWN', '2026-06-18T00:00:00Z', '2026-06-18T00:00:00Z'),
  ('ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1', 'ims.vdu_sb_sip_core.vnfc_sb_sip_core_1', 'SIP', 'DOWN', '2026-06-18T00:00:00Z', '2026-06-18T00:00:00Z'),
  ('ims.vdu_sb_sip_core.vnfc_sb_sip_core_2', 'ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1', 'SIP', 'DOWN', '2026-06-18T00:00:01Z', '2026-06-18T00:00:01Z'),
  ('ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1', 'ims.vdu_sb_sip_core.vnfc_sb_sip_core_2', 'SIP', 'DOWN', '2026-06-18T00:00:01Z', '2026-06-18T00:00:01Z'),
  ('ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1', 'ims.vdu_cs_sip_icscf.vnfc_cs_sip_icscf_1', 'SIP', 'DOWN', now(), now()),
  ('ims.vdu_cs_sip_icscf.vnfc_cs_sip_icscf_1', 'ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1', 'SIP', 'DOWN', now(), now()),
  ('ims.vdu_cs_sip_icscf.vnfc_cs_sip_icscf_1', 'ims.vdu_cs_logic.vnfc_cs_logic_1', 'HTTP2', 'DOWN', now(), now()),
  ('ims.vdu_cs_logic.vnfc_cs_logic_1', 'ims.vdu_cs_sip_icscf.vnfc_cs_sip_icscf_1', 'HTTP2', 'DOWN', now(), now()),

  ('ims.vdu_sb_diameter_core.vnfc_sb_diameter_core_1', 'ims.vdu_cs_loadbalancer_diagw.vnfc_cs_loadbalancer_diagw_1', 'DIAMETER', 'DOWN', '2026-06-18T01:00:00Z', '2026-06-18T01:00:00Z'),
  ('ims.vdu_cs_loadbalancer_diagw.vnfc_cs_loadbalancer_diagw_1', 'ims.vdu_sb_diameter_core.vnfc_sb_diameter_core_1', 'DIAMETER', 'DOWN', now(), now()),
  ('ims.vdu_sb_diameter_core.vnfc_sb_diameter_core_2', 'ims.vdu_cs_loadbalancer_diagw.vnfc_cs_loadbalancer_diagw_1', 'DIAMETER', 'DOWN', '2026-06-18T01:00:01Z', '2026-06-18T01:00:01Z'),
  ('ims.vdu_cs_loadbalancer_diagw.vnfc_cs_loadbalancer_diagw_1', 'ims.vdu_sb_diameter_core.vnfc_sb_diameter_core_2', 'DIAMETER', 'DOWN', now(), now()),
  ('ims.vdu_cs_loadbalancer_diagw.vnfc_cs_loadbalancer_diagw_1', 'ims.vdu_cs_diameter_router.vnfc_cs_diameter_router_1', 'DIAMETER', 'DOWN', now(), now()),
  ('ims.vdu_cs_diameter_router.vnfc_cs_diameter_router_1', 'ims.vdu_cs_loadbalancer_diagw.vnfc_cs_loadbalancer_diagw_1', 'DIAMETER', 'DOWN', now(), now()),
  ('ims.vdu_cs_diameter_router.vnfc_cs_diameter_router_1', 'ims.vdu_cs_diag_logic.vnfc_cs_diag_logic_1', 'HTTP2', 'DOWN', now(), now()),
  ('ims.vdu_cs_diag_logic.vnfc_cs_diag_logic_1', 'ims.vdu_cs_diameter_router.vnfc_cs_diameter_router_1', 'HTTP2', 'DOWN', now(), now()),
  ('ims.vdu_cs_diag_logic.vnfc_cs_diag_logic_1', 'ims.vdu_cs_hss_connector.vnfc_cs_hss_connector_1', 'HTTP2', 'DEGRADED', now(), now()),
  ('ims.vdu_cs_hss_connector.vnfc_cs_hss_connector_1', 'ims.vdu_cs_diag_logic.vnfc_cs_diag_logic_1', 'HTTP2', 'DEGRADED', now(), now()),

  ('ims.vdu_sb_logic.vnfc_sb_logic_1', 'ims.vdu_sb_logic.vnfc_sb_logic_2', 'SIP', 'UP', '2026-06-18T10:00:00Z', '2026-06-18T10:00:00Z')
ON CONFLICT (src_path, dst_path) DO UPDATE SET
  protocol = EXCLUDED.protocol, status = EXCLUDED.status, updated_at = EXCLUDED.updated_at;

-- ---------------------------------------------------------------------------
-- Alerts
-- ---------------------------------------------------------------------------
INSERT INTO alert (
  id, source_path, alert_type, probable_cause, perceived_severity,
  state, created_at, updated_at, additional_information
) VALUES
  ('aaaaaaaa-1111-4111-8111-111111111111', 'ims.vdu_sb_sip_core.vnfc_sb_sip_core_1', 'COMMUNICATIONS_ALERT', 'LINK_TO_PEER_SIPGW_DOWN', 'CRITICAL', 'ACTIVE', '2026-06-18T00:00:00Z', '2026-06-18T00:00:00Z', '{"dst_path":"ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1","remote_ip":"10.55.70.37","remote_port":5060,"transport":"TCP"}'),
  ('aaaaaaaa-2222-4222-8222-222222222222', 'ims.vdu_sb_sip_core.vnfc_sb_sip_core_2', 'COMMUNICATIONS_ALERT', 'LINK_TO_PEER_SIPGW_DOWN', 'CRITICAL', 'ACTIVE', '2026-06-18T00:00:01Z', '2026-06-18T00:00:01Z', '{"dst_path":"ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1","remote_ip":"10.55.70.37","remote_port":5060,"transport":"TCP"}'),
  ('eeeeeeee-1111-4111-8111-111111111111', 'ims.vdu_sb_diameter_core.vnfc_sb_diameter_core_1', 'COMMUNICATIONS_ALERT', 'LINK_TO_PEER_DIAGW_DOWN', 'CRITICAL', 'ACTIVE', '2026-06-18T01:00:00Z', '2026-06-18T01:00:00Z', '{"dst_path":"ims.vdu_cs_loadbalancer_diagw.vnfc_cs_loadbalancer_diagw_1","application_protocol":"DIAMETER","transport":"SCTP"}'),
  ('eeeeeeee-2222-4222-8222-222222222222', 'ims.vdu_sb_diameter_core.vnfc_sb_diameter_core_2', 'COMMUNICATIONS_ALERT', 'LINK_TO_PEER_DIAGW_DOWN', 'CRITICAL', 'ACTIVE', '2026-06-18T01:00:01Z', '2026-06-18T01:00:01Z', '{"dst_path":"ims.vdu_cs_loadbalancer_diagw.vnfc_cs_loadbalancer_diagw_1","application_protocol":"DIAMETER","transport":"SCTP"}'),
  ('cccccccc-3333-4333-8333-333333333333', 'ims.vdu_sb_logic.vnfc_sb_logic_1', 'QUALITY_OF_SERVICE_ALERT', 'THRESHOLD_CROSSING', 'MAJOR', 'ACTIVE', '2026-06-18T10:00:00Z', '2026-06-18T10:00:00Z', '{"metric":"overload_ram","observed_value":93.5,"threshold_value":85}'),
  ('cccccccc-3333-4333-8333-333333333334', 'ims.vdu_sb_logic.vnfc_sb_logic_1', 'QUALITY_OF_SERVICE_ALERT', 'THRESHOLD_CROSSING', 'MAJOR', 'ACTIVE', '2026-06-18T10:00:02Z', '2026-06-18T10:00:02Z', '{"metric":"overload_ram","observed_value":94.1,"threshold_value":85}'),
  ('cccccccc-3333-4333-8333-333333333335', 'ims.vdu_sb_logic.vnfc_sb_logic_1', 'QUALITY_OF_SERVICE_ALERT', 'THRESHOLD_CROSSING', 'MAJOR', 'ACTIVE', '2026-06-18T10:00:04Z', '2026-06-18T10:00:04Z', '{"metric":"overload_ram","observed_value":95.0,"threshold_value":85}')
ON CONFLICT (id) DO UPDATE SET
  source_path = EXCLUDED.source_path, alert_type = EXCLUDED.alert_type,
  probable_cause = EXCLUDED.probable_cause,
  perceived_severity = EXCLUDED.perceived_severity, state = EXCLUDED.state,
  updated_at = EXCLUDED.updated_at,
  additional_information = EXCLUDED.additional_information;

-- Expected remediation jobs from the legacy SIPGW/DIAGW fixtures. They are
-- seeded as executor test data; the GRL output itself also carries the action.
INSERT INTO activation_job (
  id, type, mo_instance, attr_path, op, value, status, created_at
) VALUES
  ('bbbbbbbb-1111-4111-8111-111111111111', 'FORWARD', 'ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1', 'lifecycle.action', 'REPLACE', '{"action":"RESTART","scenario":"LINK_TO_PEER_SIPGW_DOWN"}', 'PENDING', '2026-06-18T00:00:05Z'),
  ('cccccccc-1111-4111-8111-111111111111', 'FORWARD', 'ims.vdu_cs_sip_icscf.vnfc_cs_sip_icscf_1', 'lifecycle.action', 'REPLACE', '{"action":"RESTART","scenario":"LINK_TO_PEER_SIPGW_DOWN"}', 'PENDING', '2026-06-18T00:00:05Z'),
  ('dddddddd-1111-4111-8111-111111111111', 'FORWARD', 'ims.vdu_cs_logic.vnfc_cs_logic_1', 'lifecycle.action', 'REPLACE', '{"action":"RESTART","scenario":"LINK_TO_PEER_SIPGW_DOWN"}', 'PENDING', '2026-06-18T00:00:05Z'),
  ('ffffffff-1111-4111-8111-111111111111', 'FORWARD', 'ims.vdu_cs_loadbalancer_diagw.vnfc_cs_loadbalancer_diagw_1', 'lifecycle.action', 'REPLACE', '{"action":"RESTART","scenario":"LINK_TO_PEER_DIAGW_DOWN"}', 'PENDING', '2026-06-18T01:00:05Z'),
  ('ffffffff-2222-4222-8222-222222222222', 'FORWARD', 'ims.vdu_cs_diameter_router.vnfc_cs_diameter_router_1', 'lifecycle.action', 'REPLACE', '{"action":"RESTART","scenario":"LINK_TO_PEER_DIAGW_DOWN"}', 'PENDING', '2026-06-18T01:00:05Z'),
  ('ffffffff-3333-4333-8333-333333333333', 'FORWARD', 'ims.vdu_cs_diag_logic.vnfc_cs_diag_logic_1', 'lifecycle.action', 'REPLACE', '{"action":"RESTART","scenario":"LINK_TO_PEER_DIAGW_DOWN"}', 'PENDING', '2026-06-18T01:00:05Z')
ON CONFLICT (id) DO UPDATE SET
  mo_instance = EXCLUDED.mo_instance, attr_path = EXCLUDED.attr_path,
  op = EXCLUDED.op, value = EXCLUDED.value, status = EXCLUDED.status;

-- ---------------------------------------------------------------------------
-- Context profiles. The JSON shape follows context_profile.providers in the
-- rebuild schema. configuration contains explicit external-provider path/key/url work.
-- ---------------------------------------------------------------------------
DELETE FROM context_profile
WHERE name IN ('link_to_peer_sipgw_down_0001',
               'link_to_peer_diagw_down_0001',
               'tps_overloaded_0001');

INSERT INTO context_profile (name, description, selector, providers, enabled) VALUES
  ('link_to_peer_sipgw_down_0001', 'Context for the SIPGW Down',
   '{"probable_causes":["LINK_TO_PEER_SIPGW_DOWN"],"alert_types":["COMMUNICATIONS_ALERT"],"additional_information":{"dst_path":["ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1"]}}',
   '{"vdu":["ims.vdu_sb_sip_core","ims.vdu_cs_loadbalancer_icscf","ims.vdu_cs_sip_icscf","ims.vdu_cs_logic"],"link":[{"src_path":"ims.vdu_sb_sip_core.vnfc_sb_sip_core_1","dst_path":"ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1"}]}', TRUE),
  ('link_to_peer_diagw_down_0001', 'Context for the DIAGW Down',
   '{"probable_causes":["LINK_TO_PEER_DIAGW_DOWN"],"alert_types":["COMMUNICATIONS_ALERT"],"additional_information":{"dst_path":["ims.vdu_cs_loadbalancer_diagw.vnfc_cs_loadbalancer_diagw_1"]}}',
   '{"vdu":["ims.vdu_sb_diameter_core","ims.vdu_cs_loadbalancer_diagw","ims.vdu_cs_diameter_router","ims.vdu_cs_diag_logic","ims.vdu_cs_hss_connector"],"link":[{"src_path":"ims.vdu_sb_diameter_core.vnfc_sb_diameter_core_1","dst_path":"ims.vdu_cs_loadbalancer_diagw.vnfc_cs_loadbalancer_diagw_1"}]}', TRUE),
  ('tps_overloaded_0001', 'Context for the TPS Overloaded',
   '{"probable_causes":["THRESHOLD_CROSSING"],"alert_types":["QUALITY_OF_SERVICE_ALERT"],"additional_information":{"metric":["overload_ram"]}}',
   '{"vdu":["ims.vdu_sb_logic"],"configuration":[{"path":"ims.vdu_sb_logic.vnfc_sb_logic_1","key":"number_of_log_file","url":"http://api/v1/ims.vdu_sb_logic.vnfc_sb_logic_1/num_of_log_file"}]}', TRUE);

-- ---------------------------------------------------------------------------
-- RCA rules. One database row owns the complete scenario GRL.
-- ---------------------------------------------------------------------------
INSERT INTO rca_rule (name, description, rule_content, salience, enabled) VALUES
('link_to_sipgw_down', 'RCA and actions for LINK_TO_PEER_SIPGW_DOWN', $grl$
rule SIPGWLoadBalancerDown "The I-CSCF load balancer is unavailable" salience 100 {
    when
        Ctx.Alerts.HasCause("LINK_TO_PEER_SIPGW_DOWN") &&
        Ctx.Link.IsSeveredTo("ims.vdu_sb_sip_core", Subject.Path()) &&
        Ctx.VNFC.Parent(Subject.Path()) == "ims.vdu_cs_loadbalancer_icscf" &&
        Ctx.VNFC.IsDown(Subject.Path())
    then
        Result.Assert(
            "rc-sipgw-loadbalancer-down-" + Subject.Path(),
            "SIPGW_DOWN",
            "I-CSCF load balancer instance is TERMINATED",
            Subject.Path(),
            "PRIMARY",
            0.35
        );
        Result.Recommend(
            "rc-sipgw-loadbalancer-down-" + Subject.Path(),
            "RESTART_VNFC",
            Subject.Path(),
            "REPLACE"
        );
}

rule SIPGWICSCFDown "The I-CSCF SIP component is unavailable" salience 90 {
    when
        Ctx.Alerts.HasCause("LINK_TO_PEER_SIPGW_DOWN") &&
        Ctx.Link.IsSeveredBetween(
            "ims.vdu_sb_sip_core",
            "ims.vdu_cs_loadbalancer_icscf"
        ) &&
        Ctx.VNFC.Parent(Subject.Path()) == "ims.vdu_cs_sip_icscf" &&
        Ctx.VNFC.IsDown(Subject.Path())
    then
        Result.Assert(
            "rc-sipgw-icscf-down-" + Subject.Path(),
            "SIPGW_DOWN",
            "I-CSCF SIP component instance is TERMINATED",
            Subject.Path(),
            "PRIMARY",
            0.35
        );
        Result.Recommend(
            "rc-sipgw-icscf-down-" + Subject.Path(),
            "RESTART_VNFC",
            Subject.Path(),
            "REPLACE"
        );
}

rule SIPGWLogicDown "The SIPGW logic component is unavailable" salience 80 {
    when
        Ctx.Alerts.HasCause("LINK_TO_PEER_SIPGW_DOWN") &&
        Ctx.Link.IsSeveredBetween(
            "ims.vdu_sb_sip_core",
            "ims.vdu_cs_loadbalancer_icscf"
        ) &&
        Ctx.VNFC.Parent(Subject.Path()) == "ims.vdu_cs_logic" &&
        Ctx.VNFC.IsDown(Subject.Path())
    then
        Result.Assert(
            "rc-sipgw-logic-down-" + Subject.Path(),
            "SIPGW_DOWN",
            "SIPGW logic component instance is TERMINATED",
            Subject.Path(),
            "PRIMARY",
            0.35
        );
        Result.Recommend(
            "rc-sipgw-logic-down-" + Subject.Path(),
            "RESTART_VNFC",
            Subject.Path(),
            "REPLACE"
        );
}
$grl$, 100, TRUE),

('link_to_diagw_down', 'RCA and actions for LINK_TO_PEER_DIAGW_DOWN', $grl$
rule DIAGWLoadBalancerDown "The DIAGW load balancer is unavailable" salience 100 {
    when
        Ctx.Alerts.HasCause("LINK_TO_PEER_DIAGW_DOWN") &&
        Ctx.Link.IsSeveredTo("ims.vdu_sb_diameter_core", Subject.Path()) &&
        Ctx.VNFC.Parent(Subject.Path()) == "ims.vdu_cs_loadbalancer_diagw" &&
        Ctx.VNFC.IsDown(Subject.Path())
    then
        Result.Assert(
            "rc-diagw-loadbalancer-down-" + Subject.Path(),
            "DIAGW_DOWN",
            "DIAGW load balancer instance is TERMINATED",
            Subject.Path(),
            "PRIMARY",
            0.55
        );
        Result.Recommend(
            "rc-diagw-loadbalancer-down-" + Subject.Path(),
            "RESTART_VNFC",
            Subject.Path(),
            "REPLACE"
        );
}

rule DIAGWDiameterRouterDown "The DIAGW Diameter router is unavailable" salience 90 {
    when
        Ctx.Alerts.HasCause("LINK_TO_PEER_DIAGW_DOWN") &&
        Ctx.Link.IsSeveredBetween(
            "ims.vdu_sb_diameter_core",
            "ims.vdu_cs_loadbalancer_diagw"
        ) &&
        Ctx.VNFC.Parent(Subject.Path()) == "ims.vdu_cs_diameter_router" &&
        Ctx.VNFC.IsDown(Subject.Path())
    then
        Result.Assert(
            "rc-diagw-diameter-router-down-" + Subject.Path(),
            "DIAGW_DOWN",
            "DIAGW Diameter router instance is TERMINATED",
            Subject.Path(),
            "PRIMARY",
            0.35
        );
        Result.Recommend(
            "rc-diagw-diameter-router-down-" + Subject.Path(),
            "RESTART_VNFC",
            Subject.Path(),
            "REPLACE"
        );
}

rule DIAGWLogicDown "The DIAGW routing logic is unavailable" salience 80 {
    when
        Ctx.Alerts.HasCause("LINK_TO_PEER_DIAGW_DOWN") &&
        Ctx.Link.IsSeveredBetween(
            "ims.vdu_sb_diameter_core",
            "ims.vdu_cs_loadbalancer_diagw"
        ) &&
        Ctx.VNFC.Parent(Subject.Path()) == "ims.vdu_cs_diag_logic" &&
        Ctx.VNFC.IsDown(Subject.Path())
    then
        Result.Assert(
            "rc-diagw-logic-down-" + Subject.Path(),
            "DIAGW_DOWN",
            "DIAGW routing logic instance is TERMINATED",
            Subject.Path(),
            "PRIMARY",
            0.35
        );
        Result.Recommend(
            "rc-diagw-logic-down-" + Subject.Path(),
            "RESTART_VNFC",
            Subject.Path(),
            "REPLACE"
        );
}

rule DIAGWHSSConnectorDown "The DIAGW HSS connector is unavailable" salience 70 {
    when
        Ctx.Alerts.HasCause("LINK_TO_PEER_DIAGW_DOWN") &&
        Ctx.Link.IsSeveredBetween(
            "ims.vdu_sb_diameter_core",
            "ims.vdu_cs_loadbalancer_diagw"
        ) &&
        Ctx.VNFC.Parent(Subject.Path()) == "ims.vdu_cs_hss_connector" &&
        Ctx.VNFC.IsDown(Subject.Path())
    then
        Result.Assert(
            "rc-diagw-hss-connector-down-" + Subject.Path(),
            "DIAGW_DOWN",
            "DIAGW HSS connector instance is TERMINATED",
            Subject.Path(),
            "PRIMARY",
            0.35
        );
        Result.Recommend(
            "rc-diagw-hss-connector-down-" + Subject.Path(),
            "RESTART_VNFC",
            Subject.Path(),
            "REPLACE"
        );
}
$grl$, 100, TRUE),

('tps_overloaded', 'RCA and actions for TPS_OVERLOADED', $grl$
rule TPSReplicaDegradation "Replica degradation concentrates TPS load" salience 100 {
    when
        Ctx.Alerts.HasCause("THRESHOLD_CROSSING") &&
        Ctx.Alerts.OverloadCount("ims.vdu_sb_logic") > 1 &&
        Ctx.VDU.IsDegraded("ims.vdu_sb_logic")
    then
        Result.Assert(
            "rc-tps-replica-degradation",
            "REPLICA_DEGRADATION",
            "sb_logic is running fewer replicas than declared while TPS load is high",
            "ims.vdu_sb_logic",
            "PRIMARY",
            0.55
        );
        Result.Recommend(
            "rc-tps-replica-degradation",
            "RESTORE_REPLICAS",
            "ims.vdu_sb_logic",
            "REPLACE",
            Ctx.VDU.DesiredReplicas("ims.vdu_sb_logic")
        );
}

rule TPSHighLogFileConfiguration "High log-file count increases RAM usage" salience 90 {
    when
        Ctx.Alerts.HasCause("THRESHOLD_CROSSING") &&
        Ctx.VNFC.Parent(Subject.Path()) == "ims.vdu_sb_logic" &&
        Ctx.Alerts.HasOverload(Subject.Path()) &&
        Ctx.Configuration.Has(Subject.Path(), "number_of_log_file") &&
        Ctx.Configuration.GetFloat(Subject.Path(), "number_of_log_file") >= 3.0
    then
        Result.Assert(
            "rc-tps-high-log-file-config-" + Subject.Path(),
            "HIGH_LOG_FILE_CONFIG",
            "number_of_log_file is too high and increases RAM consumption",
            Subject.Path(),
            "CONTRIBUTING",
            0.30
        );
        Result.Recommend(
            "rc-tps-high-log-file-config-" + Subject.Path(),
            "SET_CONFIG",
            Subject.Path(),
            "REPLACE",
            3
        );
}
$grl$, 100, TRUE)
ON CONFLICT (name) DO UPDATE SET
  description = EXCLUDED.description, rule_content = EXCLUDED.rule_content,
  salience = EXCLUDED.salience, enabled = EXCLUDED.enabled,
  updated_at = now();

COMMIT;

-- External configuration provider fixture for TPS:
-- GET http://api/v1/ims.vdu_sb_logic.vnfc_sb_logic_1/num_of_log_file
-- response value: 5
