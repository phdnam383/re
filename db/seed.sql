-- Seed data for the Incident Analysis Engine (`re`).
-- Scenarios: LINK_TO_PEER_SIPGW_DOWN, LINK_TO_H248GW_DOWN, LINK_TO_LOGIC_DOWN.
--
-- The engine is read-only on PostgreSQL and touches exactly two tables:
--   - context_profile (see internal/contextbuilder/postgres/profile.go)
--   - rca_rule        (see internal/ruleengine/postgres/rule.go)
-- Everything else flows in out of band:
--   - VDU/VNFC state         -> HTTP VDU provider
--   - configuration          -> HTTP configuration provider (currentValue served
--                               externally; not stored in this DB)
--   - link/metric            -> HTTP link/metric providers
--   - the alert itself       -> the AnalyzeAlertByRule gRPC request
-- So this file seeds only those two tables. Topology, alerts, and
-- activation_job are intentionally NOT seeded here.

BEGIN;

-- ---------------------------------------------------------------------------
-- Context profiles. The JSON shape follows context_profile.providers in the
-- schema. configuration lists keys resolved against the alert's VDU path.
-- ---------------------------------------------------------------------------
DELETE FROM context_profile
WHERE name IN ('link_to_peer_sipgw_down_0001',
               'link_to_h248gw_down_0001',
               'link_to_logic_down_0001');

INSERT INTO context_profile (name, description, selector, providers, enabled) VALUES
  ('link_to_peer_sipgw_down_0001', 'Context for the SIPGW Down',
 '{"probable_causes":["LINK_TO_PEER_SIPGW_DOWN"],"alert_types":["COMMUNICATIONS_ALERT"],"additional_information":{"dst_path":["ims.vdu_cs_loadbalancer_icscf"]}}',
 '{"vdu":["ims.vdu_sb_sip_core","ims.vdu_cs_loadbalancer_icscf","ims.vdu_cs_sip_icscf","ims.vdu_cs_logic"],"link":[{"target":"10.55.70.37"}]}', TRUE),
  ('link_to_h248gw_down_0001', 'Context for the link to the SB H248 gateway down. Carries the alerting peer VDU and the H248 gateway VDU whose VNFCs go unavailable when the link is severed.',
   '{"probable_causes":["LINK_TO_H248GW_DOWN"],"alert_types":["COMMUNICATIONS_ALERT"],"additional_information":{"dst_path":["ims.vdu_sb_h248gw"]}}',
   '{"vdu":["ims.vdu_sb_sip_core","ims.vdu_sb_h248gw"]}', TRUE),
  ('link_to_logic_down_0001', 'Context for the SB Logic Down',
   '{"probable_causes":["LINK_TO_LOGIC_DOWN"],"alert_types":["COMMUNICATIONS_ALERT"],"additional_information":{"dst_path":["ims.vdu_sb_logic"]}}',
   '{"vdu":["ims.vdu_sb_logic"]}', TRUE);

-- ---------------------------------------------------------------------------
-- RCA rules. One database row owns the complete scenario GRL.
-- VDU/VNFC paths referenced below are plain strings evaluated against the
-- context snapshot (served by the HTTP providers); they are not FKs into
-- seeded topology here.
-- ---------------------------------------------------------------------------
INSERT INTO rca_rule (name, description, rule_content, salience, enabled) VALUES
('link_to_sipgw_down', 'RCA and actions for LINK_TO_PEER_SIPGW_DOWN', $grl$
rule SIPGWLoadBalancerDown "The I-CSCF load balancer is unavailable" salience 100 {
    when
        Ctx.Alert.HasCause("LINK_TO_PEER_SIPGW_DOWN") &&
        Ctx.Alert.VduPath() == "ims.vdu_sb_sip_core" &&
        Ctx.Link.PingFails("10.55.70.37") &&
        Ctx.Vnfc.HasAnyDownInVDU("ims.vdu_cs_loadbalancer_icscf")
    then
        Result.Assert(
            "SIPGW_DOWN",
            "PRIMARY",
            "I-CSCF load balancer is unavailable"
        );
        Result.RecommendRestartVNFC(
            Ctx.Vnfc.DownPathsInVDU("ims.vdu_cs_loadbalancer_icscf")
        );
}

rule SIPGWICSCFDown "The I-CSCF SIP component is unavailable" salience 90 {
    when
        Ctx.Alert.HasCause("LINK_TO_PEER_SIPGW_DOWN") &&
        Ctx.Alert.VduPath() == "ims.vdu_sb_sip_core" &&
        Ctx.Vnfc.HasAnyDownInVDU("ims.vdu_cs_sip_icscf")
    then
        Result.Assert(
            "SIPGW_DOWN",
            "PRIMARY",
            "I-CSCF SIP component is unavailable"
        );
        Result.RecommendRestartVNFC(
            Ctx.Vnfc.DownPathsInVDU("ims.vdu_cs_sip_icscf")
        );
}

rule SIPGWLogicDown "The SIPGW logic component is unavailable" salience 80 {
    when
        Ctx.Alert.HasCause("LINK_TO_PEER_SIPGW_DOWN") &&
        Ctx.Alert.VduPath() == "ims.vdu_sb_sip_core" &&
        Ctx.Vnfc.HasAnyDownInVDU("ims.vdu_cs_logic")
    then
        Result.Assert(
            "SIPGW_DOWN",
            "PRIMARY",
            "SIPGW logic component is unavailable"
        );
        Result.RecommendRestartVNFC(
            Ctx.Vnfc.DownPathsInVDU("ims.vdu_cs_logic")
        );
}

$grl$, 100, TRUE),

('link_to_h248gw_down', 'RCA and actions for LINK_TO_H248GW_DOWN', $grl$
rule SBH248GWDown "The SB H248 gateway is unavailable" salience 100 {
    when
        Ctx.Alert.HasCause("LINK_TO_H248GW_DOWN") &&
        Ctx.Vnfc.HasAnyDownInVDU("ims.vdu_sb_h248gw")
    then
        Result.Assert(
            "SB_H248GW_DOWN",
            "PRIMARY",
            "SB H248 gateway is unavailable"
        );
        Result.RecommendRestartVNFC(
            Ctx.Vnfc.DownPathsInVDU("ims.vdu_sb_h248gw")
        );
}
$grl$, 100, TRUE),

('link_to_logic_down', 'RCA and actions for LINK_TO_LOGIC_DOWN', $grl$
rule SBLogicDown "The SB logic component is unavailable" salience 100 {
    when
        Ctx.Alert.HasCause("LINK_TO_LOGIC_DOWN") &&
        Ctx.Vnfc.HasAnyDownInVDU("ims.vdu_sb_logic")
    then
        Result.Assert(
            "SB_LOGIC_DOWN",
            "PRIMARY",
            "SB logic component is unavailable"
        );
        Result.RecommendRestartVNFC(
            Ctx.Vnfc.DownPathsInVDU("ims.vdu_sb_logic")
        );
}
$grl$, 100, TRUE)
ON CONFLICT (name) DO UPDATE SET
  description = EXCLUDED.description, rule_content = EXCLUDED.rule_content,
  salience = EXCLUDED.salience, enabled = EXCLUDED.enabled,
  updated_at = now();

COMMIT;
