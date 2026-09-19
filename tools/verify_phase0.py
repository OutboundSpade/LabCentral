#!/usr/bin/env python3
"""Verify LabCentral's design-only Phase 0 contracts.

Prerequisites: Python 3 with PyYAML and jsonschema, Bun, and Buf. Set BUFBIN
or BUN when the executables are not on PATH. This tool does not execute any
provider operation or production application code.
"""

from __future__ import annotations

import base64
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
from typing import Any

import jsonschema
import yaml


ROOT = Path(__file__).resolve().parents[1]


class DuplicateKeyError(ValueError):
    pass


class UniqueKeyLoader(yaml.SafeLoader):
    pass


def construct_unique_mapping(loader: UniqueKeyLoader, node: yaml.MappingNode, deep: bool = False) -> dict[Any, Any]:
    loader.flatten_mapping(node)
    result: dict[Any, Any] = {}
    for key_node, value_node in node.value:
        key = loader.construct_object(key_node, deep=deep)
        if key in result:
            raise DuplicateKeyError(f"duplicate YAML key {key!r} at line {key_node.start_mark.line + 1}")
        result[key] = loader.construct_object(value_node, deep=deep)
    return result


UniqueKeyLoader.add_constructor(
    yaml.resolver.BaseResolver.DEFAULT_MAPPING_TAG,
    construct_unique_mapping,
)


def load_yaml(path: Path) -> Any:
    return yaml.load(path.read_text(encoding="utf-8"), Loader=UniqueKeyLoader)


def load_yaml_documents(path: Path) -> list[Any]:
    return list(yaml.load_all(path.read_text(encoding="utf-8"), Loader=UniqueKeyLoader))


def run(command: list[str], *, input_text: str | None = None) -> str:
    completed = subprocess.run(
        command,
        cwd=ROOT,
        input=input_text,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    if completed.returncode != 0:
        rendered = " ".join(command)
        raise AssertionError(f"command failed ({completed.returncode}): {rendered}\n{completed.stdout}{completed.stderr}")
    return completed.stdout


def executable(environment_name: str, default_name: str) -> str:
    configured = os.environ.get(environment_name)
    if configured:
        if not Path(configured).is_file():
            raise AssertionError(f"{environment_name} does not name a file: {configured}")
        return configured
    found = shutil.which(default_name)
    if not found:
        raise AssertionError(f"missing {default_name}; install it or set {environment_name}")
    return found


def verify_yaml_and_json() -> None:
    yaml_paths = [
        *ROOT.glob("design/*.yaml"),
        *ROOT.glob("design/contracts/*.yaml"),
        *ROOT.glob("examples/*/bundle.yaml"),
        *ROOT.glob("examples/*/lab.yaml"),
        *ROOT.glob("examples/routed-cyber-lab/guest/*.yaml"),
        ROOT / "api/plugin/v1/compat/compatibility.yaml",
        ROOT / "schemas/fixtures/lab/invalid/expected-errors.yaml",
        ROOT / "schemas/fixtures/parser/cases.yaml",
        ROOT / "schemas/fixtures/canonical/vectors.yaml",
    ]
    for path in yaml_paths:
        load_yaml(path)
    for path in ROOT.glob("deploy/kubernetes/*.yaml"):
        load_yaml_documents(path)
    for path in ROOT.glob(".github/workflows/*.yml"):
        load_yaml(path)
    json.loads((ROOT / "schemas/lab-v1alpha1.schema.json").read_text(encoding="utf-8"))
    for path in ROOT.glob("design/contracts/*.json"):
        schema = json.loads(path.read_text(encoding="utf-8"))
        jsonschema.Draft202012Validator.check_schema(schema)
    t3_schema = json.loads((ROOT / "design/contracts/proxmox-t3-smoke-profile.schema.json").read_text(encoding="utf-8"))
    t3_profile = json.loads((ROOT / "schemas/fixtures/proxmox/t3-smoke-profile.valid.json").read_text(encoding="utf-8"))
    jsonschema.Draft202012Validator(t3_schema).validate(t3_profile)
    isolation_schema = json.loads(
        (ROOT / "design/contracts/proxmox-t3-isolation-profile.schema.json").read_text(encoding="utf-8")
    )
    isolation_profile = json.loads(
        (ROOT / "schemas/fixtures/proxmox/t3-isolation-profile.valid.json").read_text(encoding="utf-8")
    )
    jsonschema.Draft202012Validator(isolation_schema).validate(isolation_profile)
    for name in ["request", "response"]:
        probe_schema = json.loads(
            (ROOT / f"design/contracts/proxmox-qualification-probe-{name}.schema.json").read_text(encoding="utf-8")
        )
        probe_fixture = json.loads(
            (ROOT / f"schemas/fixtures/proxmox/qualification-probe-{name}.valid.json").read_text(encoding="utf-8")
        )
        jsonschema.Draft202012Validator(probe_schema).validate(probe_fixture)
    for path in ROOT.glob("schemas/fixtures/canonical/*.json"):
        json.loads(path.read_text(encoding="utf-8"))


def verify_bundles() -> None:
    for manifest_path in ROOT.glob("examples/*/bundle.yaml"):
        manifest = load_yaml(manifest_path)
        listed: set[str] = set()
        for entry in manifest["files"]:
            relative = entry["path"]
            assert relative not in listed, f"duplicate manifest entry: {manifest_path}: {relative}"
            listed.add(relative)
            file_path = manifest_path.parent / relative
            data = file_path.read_bytes()
            assert len(data) == entry["size"], f"size mismatch: {file_path}"
            assert hashlib.sha256(data).hexdigest() == entry["sha256"], f"digest mismatch: {file_path}"


def verify_lab_schema() -> None:
    schema = json.loads((ROOT / "schemas/lab-v1alpha1.schema.json").read_text(encoding="utf-8"))
    validator_class = jsonschema.validators.validator_for(schema)
    validator_class.check_schema(schema)
    validator = validator_class(schema)

    for path in ROOT.glob("examples/*/lab.yaml"):
        errors = list(validator.iter_errors(load_yaml(path)))
        assert not errors, "\n".join(f"{path}: {list(error.path)}: {error.message}" for error in errors)

    fixture_root = ROOT / "schemas/fixtures/lab/invalid"
    cases = load_yaml(fixture_root / "expected-errors.yaml")["cases"]
    for case in cases:
        path = fixture_root / case["file"]
        errors = list(validator.iter_errors(load_yaml(path)))
        validators = {error.validator for error in errors}
        assert errors, f"invalid fixture was accepted: {case['id']}"
        assert case["expectedKeyword"] in validators, (
            f"{case['id']} expected {case['expectedKeyword']}, observed {sorted(validators)}"
        )

    parser_cases = load_yaml(ROOT / "schemas/fixtures/parser/cases.yaml")["cases"]
    for case in parser_cases:
        path = ROOT / "schemas/fixtures/parser" / case["file"]
        try:
            load_yaml(path)
        except DuplicateKeyError:
            continue
        raise AssertionError(f"parser fixture was accepted: {case['id']}")


def canonicalize(bun: str, payload: str) -> str:
    return run([bun, "tools/jcs.mjs"], input_text=payload)


def verify_canonical_vectors(bun: str) -> None:
    fixture_root = ROOT / "schemas/fixtures/canonical"
    manifest = load_yaml(fixture_root / "vectors.yaml")
    format_version = manifest["canonicalFormatVersion"]
    for vector in manifest["vectors"]:
        expected_path = fixture_root / vector["expected"]
        expected = canonicalize(bun, expected_path.read_text(encoding="utf-8"))
        for input_name in vector["inputs"]:
            input_path = fixture_root / input_name
            if input_path.suffix in {".yaml", ".yml"}:
                parsed = load_yaml(input_path)
                payload = json.dumps(parsed, ensure_ascii=False, separators=(",", ":"), allow_nan=False)
            else:
                payload = input_path.read_text(encoding="utf-8")
            actual = canonicalize(bun, payload)
            assert actual == expected, f"canonical mismatch: {vector['id']}: {input_name}"
        digest = hashlib.sha256(format_version.encode() + b"\x00" + expected.encode()).hexdigest()
        assert digest == vector["expectedDigest"], f"digest mismatch: {vector['id']}"


def select_protocol(host_ranges: list[dict[str, int]], plugin_ranges: list[dict[str, int]]) -> dict[str, int] | None:
    candidates: list[tuple[int, int]] = []
    for host in host_ranges:
        for plugin in plugin_ranges:
            if host["major"] != plugin["major"]:
                continue
            lower = max(host["minMinor"], plugin["minMinor"])
            upper = min(host["maxMinor"], plugin["maxMinor"])
            if lower <= upper:
                candidates.append((host["major"], upper))
    if not candidates:
        return None
    major, minor = max(candidates)
    return {"major": major, "minor": minor}


def verify_plugin_cases() -> None:
    cases = load_yaml(ROOT / "api/plugin/v1/compat/compatibility.yaml")["cases"]
    for case in cases:
        kind = case["kind"]
        expected = case["expected"]
        if kind == "negotiate":
            selected = select_protocol(case["hostRanges"], case["pluginRanges"])
            actual = {"selected": selected} if selected else {"error": "protocol_incompatible"}
        elif kind == "identity":
            if case["launchTokenState"] != "valid":
                actual = {"error": "launch_token_invalid"}
            elif case["configuredPluginID"] != case["reportedPluginID"]:
                actual = {"error": "plugin_identity_mismatch"}
            else:
                actual = {"result": "accepted"}
        elif kind == "deadline":
            if case["deadline"] == "missing":
                actual = {"error": "invocation_deadline_required"}
            elif case["deadline"] == "expired":
                actual = {"error": "invocation_deadline_expired"}
            else:
                actual = {"operationState": "unknown", "nextAction": "get_invocation_before_retry"}
        elif kind == "cancellation":
            if case["invocationState"] == "before_unsafe_boundary":
                actual = {"rpcOutcome": "OUTCOME_REQUESTED", "operationState": "canceled"}
            else:
                actual = {"rpcOutcome": "OUTCOME_TOO_LATE", "operationState": "running"}
        elif kind == "providerState":
            if case["storedMajor"] in case["readableMajors"]:
                actual = {"result": "readable"}
            else:
                actual = {"error": "state_version_incompatible"}
        elif kind == "feature":
            selected = case["selectedProtocol"]
            missing = set(case["requiredFeatures"]) - set(case["pluginFeatures"])
            if selected["major"] != 1 or selected["minor"] < case["requiredMinor"]:
                actual = {"error": "protocol_feature_unavailable"}
            elif missing:
                actual = {"error": "plugin_feature_unavailable"}
            else:
                actual = {"result": "accepted"}
        else:
            raise AssertionError(f"unknown plugin fixture kind: {kind}")
        assert actual == expected, f"plugin fixture mismatch: {case['id']}: {actual!r} != {expected!r}"


def verify_proto(buf: str) -> None:
    compatibility = load_yaml(ROOT / "api/plugin/v1/compat/compatibility.yaml")
    baseline = compatibility["descriptorBaseline"]
    encoded_baseline = b"".join((ROOT / baseline["path"]).read_bytes().split())
    baseline_bytes = base64.b64decode(encoded_baseline, validate=True)
    assert hashlib.sha256(baseline_bytes).hexdigest() == baseline["sha256"], "baseline descriptor digest mismatch"
    run([buf, "format", "-d", "--exit-code", "api"])
    run([buf, "lint", "api"])
    with tempfile.TemporaryDirectory(prefix="labcentral-phase0-") as temporary:
        temporary_path = Path(temporary)
        baseline_path = temporary_path / "plugin-v1-baseline.binpb"
        current_path = temporary_path / "plugin-v1-current.binpb"
        baseline_path.write_bytes(baseline_bytes)
        run([
            buf,
            "build",
            "api",
            "--exclude-source-info",
            "--as-file-descriptor-set",
            "-o",
            str(current_path),
        ])
        current = compatibility["currentDescriptor"]
        assert current["protocol"] == {"major": 1, "minor": 2}, "current plugin protocol version is not 1.2"
        assert hashlib.sha256(current_path.read_bytes()).hexdigest() == current["sha256"], (
            "current plugin descriptor digest is stale; verify compatibility before refreshing it"
        )
        run([buf, "breaking", "api", "--against", str(baseline_path)])


def collect_test_ids() -> set[str]:
    ids: set[str] = set()
    sources = [
        ROOT / "api/plugin/v1/compat/compatibility.yaml",
        ROOT / "design/contracts/provider-conformance.yaml",
        ROOT / "design/contracts/proxmox-qemu-mutation.yaml",
        ROOT / "design/contracts/allocation-claims.yaml",
        ROOT / "design/contracts/rendering-cases.yaml",
        ROOT / "design/contracts/job-invariants.yaml",
        ROOT / "design/contracts/access-conformance.yaml",
        ROOT / "design/contracts/production-observability.yaml",
        ROOT / "design/contracts/production-topology.yaml",
        ROOT / "design/contracts/production-recovery.yaml",
        ROOT / "design/contracts/production-resilience.yaml",
        ROOT / "design/contracts/production-release.yaml",
        ROOT / "schemas/fixtures/lab/invalid/expected-errors.yaml",
        ROOT / "schemas/fixtures/parser/cases.yaml",
        ROOT / "schemas/fixtures/canonical/vectors.yaml",
    ]
    for source in sources:
        document = load_yaml(source)
        entries = document.get("cases", document.get("requiredCases", document.get("invariants", document.get("vectors", []))))
        for entry in entries:
            test_id = entry["id"]
            assert test_id not in ids, f"duplicate test ID: {test_id}"
            ids.add(test_id)
    security = load_yaml(ROOT / "design/contracts/security-controls.yaml")
    for definition in security["testDefinitions"]:
        test_id = definition["id"]
        assert test_id not in ids, f"duplicate test ID: {test_id}"
        ids.add(test_id)
    return ids


def verify_proxmox_mutation_contract() -> None:
    contract = load_yaml(ROOT / "design/contracts/proxmox-qemu-mutation.yaml")
    assert contract["suite"] == "labcentral.proxmox-qemu-mutation/v1alpha1"
    assert contract["status"] == "single_operator_production_release_candidate_verified"
    assert contract["schemas"] == {
        "targetConfig": {"name": "proxmox.target-config", "version": 1.1},
        "plan": {"name": "proxmox.qemu-plan", "version": 1.0},
        "providerState": {"name": "proxmox.qemu-state", "version": 1.0},
        "invocationJournal": {"name": "proxmox.invocation-journal", "version": 1.0},
    }
    required_features = {
        "stable_instance_identity_before_plan",
        "opaque-allocation-claims-v1",
        "excluded_claims_on_replan",
        "dependency-resource-input-v1",
        "durable_get_invocation",
    }
    assert set(contract["requiredProtocolFeatures"]) == required_features
    assert contract["allocationClaims"]["uniqueKey"] == ["targetID", "namespace", "value"]
    assert contract["allocationClaims"]["releaseCondition"] == "provider_absence_confirmed"
    qualification = contract["qualificationEvidence"]
    assert qualification["status"] == "exact_release_testbed_signed_and_verified"
    assert qualification["evidenceSchema"] == "labcentral.proxmox-qualification-evidence/v1"
    assert qualification["envelopeSchema"] == "labcentral.proxmox-qualification-envelope/v1"
    assert qualification["canonicalization"] == "RFC_8785"
    assert qualification["signature"] == {
        "algorithm": "Ed25519",
        "encoding": "unpadded_base64url",
        "domainSeparated": True,
    }
    assert qualification["requiredT3Cases"] == [
        "TEST-PROXMOX-003",
        "TEST-PROXMOX-004",
        "TEST-PROXMOX-005",
        "TEST-PROXMOX-007",
        "TEST-PROXMOX-008",
        "TEST-PROXMOX-009",
        "TEST-PROXMOX-010",
        "TEST-PROXMOX-011",
        "TEST-PROXMOX-012",
        "TEST-PROXMOX-013",
        "TEST-PROXMOX-014",
        "TEST-PROXMOX-015",
        "TEST-PROXMOX-017",
        "TEST-PROXMOX-018",
        "TEST-PROXMOX-019",
        "TEST-PROXMOX-022",
    ]
    assert qualification["productionEnablement"] == "adr_0014_authorizer_implemented_exact_release_candidate_verified"
    assert contract["productionAuthorization"] == {
        "status": "implemented_fail_closed",
        "decision": "ADR-0014",
        "trustSchema": "labcentral.proxmox-qualification-trust/v1",
        "trustSchemaFile": "design/contracts/proxmox-qualification-trust.schema.json",
        "defaultConstructor": "discovery_only",
        "writeConstructor": "requires_non_nil_mutation_authorizer",
        "checks": [
            "active_non_revoked_reviewer_key_and_deployment_maximum_age",
            "complete_passing_signed_case_probe_and_cleanup_evidence",
            "exact_target_installation_configuration_capability_inventory_and_policy",
            "exact_pve_release_build_provider_binary_and_source_tree",
            "exact_protocol_plugin_and_normalized_live_profile",
        ],
        "evaluationPoints": ["capability_discovery", "apply", "destroy", "lifecycle"],
        "failureBehavior": "unsupported_or_qualification_gate_closed_before_write",
    }
    smoke = contract["credentialedSmokeHarness"]
    assert smoke == {
        "status": "implemented_executed_two_storage_profiles",
        "profileSchema": "labcentral.proxmox-t3-smoke-profile/v1",
        "profileSchemaFile": "design/contracts/proxmox-t3-smoke-profile.schema.json",
        "activation": {
            "exactSentinel": "I_ACKNOWLEDGE_LABCENTRAL_PROXMOX_T3_DESTRUCTIVE",
            "targetSpecificConfirmation": "required",
            "absoluteDistinctInputPaths": "required",
        },
        "operations": [
            "read_only_discovery_and_exact_preflight",
            "plan_and_bind_confirmed_disposable_vmid_and_vlan",
            "create_network_and_single_qemu_full_clone",
            "stop_start_pause_resume",
            "ownership_checked_reverse_destroy",
            "provider_absence_confirmation",
        ],
        "cleanupTimeoutSeconds": 300,
        "defaultBehavior": "skipped_with_zero_provider_calls",
        "qualifyingEvidence": False,
    }
    failure_recovery = contract["credentialedFailureRecoveryHarness"]
    assert failure_recovery == {
        "status": "implemented_executed_exact_release",
        "profileSchema": "labcentral.proxmox-t3-smoke-profile/v1",
        "profileSchemaFile": "design/contracts/proxmox-t3-smoke-profile.schema.json",
        "activation": {
            "exactSentinel": "I_ACKNOWLEDGE_LABCENTRAL_PROXMOX_T3_DESTRUCTIVE",
            "targetSpecificConfirmation": "required",
            "absoluteDistinctInputPaths": 3,
        },
        "boundaryFaults": [
            "permission_or_inventory_read_substitution",
            "response_loss_after_confirmed_PVE_acceptance",
            "synthetic_failed_task_before_object_creation",
            "stale_config_digest_rejection_by_PVE",
            "compensation_delete_rejection_before_PVE_request",
        ],
        "guarantees": {
            "injectedAndNativeOutcomesDistinguished": True,
            "mutationCallsCounted": True,
            "everyDurableCheckpointRestarted": True,
            "corruptJournalFailsClosed": True,
            "ownedCleanupTimeoutSeconds": 600,
            "providerAbsenceRechecked": True,
        },
        "defaultBehavior": "skipped_with_zero_provider_calls",
        "qualifyingEvidence": False,
    }
    upgrade = contract["upgradeReadabilityHarness"]
    assert upgrade == {
        "status": "implemented_executed_read_only",
        "activation": {
            "exactSentinel": "I_ACKNOWLEDGE_LABCENTRAL_PROXMOX_T4_READ_ONLY_UPGRADE",
            "absoluteJournalDirectory": "required",
            "exactTargetID": "required",
            "priorBuildInvocationIDs": "3_to_32_distinct",
        },
        "coverage": [
            "checksummed_invocation_journal_v1_0",
            "network_provider_state_v1_0",
            "vm_provider_state_v1_0",
            "destroyed_tombstone",
        ],
        "providerClientConstructed": False,
        "mutationPath": "none",
        "defaultBehavior": "skipped_before_journal_access",
    }
    qualification_probe = contract["qualificationProbe"]
    assert qualification_probe == {
        "status": "protocol_agent_and_client_executed_on_guest",
        "requestSchema": "labcentral.proxmox-qualification-probe-request/v1",
        "requestSchemaFile": "design/contracts/proxmox-qualification-probe-request.schema.json",
        "responseSchema": "labcentral.proxmox-qualification-probe-response/v1",
        "responseSchemaFile": "design/contracts/proxmox-qualification-probe-response.schema.json",
        "transport": {
            "protocol": "HTTPS_HTTP_1_1",
            "tlsMinimum": "1.3",
            "clientAuthentication": "dedicated_CA_mTLS_required",
            "agentOrigin": "exact_non_loopback_ipv4_and_unprivileged_port",
            "redirectsAndProxies": "disabled",
        },
        "operation": {
            "method": "POST",
            "path": "/v1/probes",
            "behavior": "bounded_TCP_connect_only",
            "shellOrGuestExec": False,
            "singleFlight": True,
        },
        "limits": {
            "bodyBytes": 16384,
            "orderedTargets": ["intended_segment", "management", "cross_instance"],
            "attempts": {"minimum": 3, "maximum": 10},
            "timeoutMillis": {"minimum": 100, "maximum": 5000},
        },
        "evidence": {
            "nonceBound": True,
            "agentImageDigestBound": True,
            "rawTargetAddressesReturned": False,
            "canonicalTranscriptDigest": "sha256",
        },
        "productionRole": "none_qualification_fixture_only",
    }
    isolation = contract["credentialedIsolationHarness"]
    assert isolation == {
        "status": "implemented_executed_exact_release",
        "profileSchema": "labcentral.proxmox-t3-isolation-profile/v1",
        "profileSchemaFile": "design/contracts/proxmox-t3-isolation-profile.schema.json",
        "activation": {
            "exactSentinel": "I_ACKNOWLEDGE_LABCENTRAL_PROXMOX_T3_DESTRUCTIVE",
            "targetSpecificConfirmation": "required",
            "absoluteDistinctInputPaths": 6,
        },
        "topology": {
            "sourceInstance": {"networks": 1, "guests": 2},
            "crossInstance": {"networks": 1, "guests": 1},
            "distinctVMIDs": 3,
            "distinctVLANs": 2,
        },
        "probe": {
            "origin": "source_guest_exact_ipv4",
            "destinations": ["intended_peer", "management", "cross_instance_guest"],
            "evidenceMapping": "typed_probe_records_for_signed_evidence",
        },
        "cleanup": {
            "order": "reverse_dependency",
            "perResourceTimeoutSeconds": 600,
            "vmAbsenceRechecked": True,
        },
        "defaultBehavior": "skipped_with_zero_provider_calls",
        "qualifyingEvidence": False,
    }

    allowed_methods = {"GET", "POST", "DELETE"}
    allowed_paths = {
        "/access/permissions",
        "/cluster/nextid",
        "/nodes/{node}/qemu/{vmid}/config",
        "/nodes/{node}/qemu/{vmid}/clone",
        "/nodes/{node}/qemu/{vmid}/status/current",
        "/nodes/{node}/qemu/{vmid}/status/start",
        "/nodes/{node}/qemu/{vmid}/status/shutdown",
        "/nodes/{node}/qemu/{vmid}/status/suspend",
        "/nodes/{node}/qemu/{vmid}/status/resume",
        "/nodes/{node}/qemu/{vmid}/agent/ping",
        "/nodes/{node}/tasks/{upid}/status",
        "/nodes/{node}/qemu/{vmid}",
    }
    for call in contract["allowedApi"]:
        assert call["method"] in allowed_methods, f"unexpected Proxmox mutation method: {call}"
        assert call["path"] in allowed_paths, f"unexpected Proxmox mutation path: {call}"

    provider_ids = {
        case["id"] for case in load_yaml(ROOT / "design/contracts/provider-conformance.yaml")["requiredCases"]
    }
    assert set(contract["covers"]) == provider_ids, "Proxmox contract must map every generic provider case"
    cases = contract["cases"]
    case_ids = [case["id"] for case in cases]
    assert len(cases) >= 20, "Proxmox mutation contract lost required failure-window coverage"
    assert len(case_ids) == len(set(case_ids)), "duplicate Proxmox mutation test ID"
    referenced = {test_id for mapped in contract["covers"].values() for test_id in mapped}
    assert referenced <= set(case_ids), "Proxmox generic coverage references an undefined case"
    for case in cases:
        binding = case.get("testBinding", "")
        assert "#" in binding, f"{case['id']} has no executable test binding"
        relative_path, test_name = binding.split("#", 1)
        test_path = ROOT / relative_path
        assert test_path.is_file(), f"{case['id']} test binding file is missing: {relative_path}"
        source = test_path.read_text(encoding="utf-8")
        top_level_test = test_name.split("/", 1)[0]
        assert f"func {top_level_test}(" in source, f"{case['id']} test function is missing: {top_level_test}"
        if "/" in test_name:
            assert case["id"] in source, f"{case['id']} subtest binding is missing"
        credentialed_binding = case.get("credentialedBinding")
        if credentialed_binding:
            assert "#" in credentialed_binding, f"{case['id']} credentialed binding is invalid"
            relative_path, test_name = credentialed_binding.split("#", 1)
            test_path = ROOT / relative_path
            assert test_path.is_file(), f"{case['id']} credentialed binding file is missing: {relative_path}"
            source = test_path.read_text(encoding="utf-8")
            assert f"func {test_name}(" in source, f"{case['id']} credentialed test function is missing: {test_name}"
        companion_binding = case.get("companionBinding")
        if companion_binding:
            assert "#" in companion_binding, f"{case['id']} companion binding is invalid"
            relative_path, test_name = companion_binding.split("#", 1)
            test_path = ROOT / relative_path
            assert test_path.is_file(), f"{case['id']} companion binding file is missing: {relative_path}"
            source = test_path.read_text(encoding="utf-8")
            assert f"func {test_name}(" in source, f"{case['id']} companion test function is missing: {test_name}"
        evidence_binding = case.get("evidenceBinding")
        if evidence_binding:
            assert "#" in evidence_binding, f"{case['id']} evidence binding is invalid"
            relative_path, test_name = evidence_binding.split("#", 1)
            test_path = ROOT / relative_path
            assert test_path.is_file(), f"{case['id']} evidence binding file is missing: {relative_path}"
            source = test_path.read_text(encoding="utf-8")
            assert f"func {test_name}(" in source, f"{case['id']} evidence test function is missing: {test_name}"

    before_gate = contract["capabilityGate"]["beforeGate"]
    assert before_gate, "Proxmox mutation capability gate is empty"
    assert set(before_gate.values()) == {"unsupported"}, "mutation capabilities must stay unsupported before gates"


def verify_contract_catalogs() -> None:
    tests = collect_test_ids()
    provider = load_yaml(ROOT / "design/contracts/provider-conformance.yaml")
    assert len(provider["requiredCases"]) >= 18, "provider suite lost required failure coverage"
    rendering = load_yaml(ROOT / "design/contracts/rendering-cases.yaml")
    go_test_source = "\n".join(
        path.read_text(encoding="utf-8") for path in ROOT.rglob("*_test.go")
        if ".git" not in path.parts
    )
    provider_suite_source = (ROOT / "pkg/plugin-sdk-go/contracttest/suite.go").read_text(encoding="utf-8")
    for case in provider["requiredCases"]:
        go_test = case.get("goTest")
        assert go_test, f"provider case has no Go test binding: {case['id']}"
        assert re.search(rf"func\s+{re.escape(go_test)}\s*\(", go_test_source), (
            f"provider case references missing Go test: {case['id']}: {go_test}"
        )
        assert f'"{case["id"]}"' in provider_suite_source, (
            f"provider case is missing from the shared contract runner: {case['id']}"
        )
    for case in rendering["cases"]:
        go_test = case.get("goTest")
        assert go_test, f"rendering case has no Go test binding: {case['id']}"
        assert re.search(rf"func\s+{re.escape(go_test)}\s*\(", go_test_source), (
            f"rendering case references missing Go test: {case['id']}: {go_test}"
        )
    jobs = load_yaml(ROOT / "design/contracts/job-invariants.yaml")
    for invariant in jobs["invariants"]:
        go_test = invariant.get("goTest")
        assert go_test, f"job invariant has no Go test binding: {invariant['id']}"
        assert re.search(rf"func\s+{re.escape(go_test)}\s*\(", go_test_source), (
            f"job invariant references missing Go test: {invariant['id']}: {go_test}"
        )
        postgresql_test = invariant.get("postgresqlGoTest")
        if postgresql_test:
            assert re.search(rf"func\s+{re.escape(postgresql_test)}\s*\(", go_test_source), (
                f"job invariant references missing PostgreSQL test: {invariant['id']}: {postgresql_test}"
            )
    allocations = load_yaml(ROOT / "design/contracts/allocation-claims.yaml")
    assert allocations["uniqueKey"] == ["targetID", "namespace", "value"]
    assert allocations["states"] == ["held", "committed", "orphaned", "released"]
    assert len(allocations["cases"]) >= 14, "allocation suite lost required transition coverage"
    for case in allocations["cases"]:
        go_test = case.get("goTest")
        assert go_test, f"allocation case has no Go test binding: {case['id']}"
        assert re.search(rf"func\s+{re.escape(go_test)}\s*\(", go_test_source), (
            f"allocation case references missing Go test: {case['id']}: {go_test}"
        )
        postgresql_test = case.get("postgresqlGoTest")
        if postgresql_test:
            assert re.search(rf"func\s+{re.escape(postgresql_test)}\s*\(", go_test_source), (
                f"allocation case references missing PostgreSQL test: {case['id']}: {postgresql_test}"
            )
    access = load_yaml(ROOT / "design/contracts/access-conformance.yaml")
    assert access["states"] == ["pending", "ready", "active", "expired", "revoked", "failed"]
    assert {case["id"] for case in access["cases"]} == {f"TEST-ACCESS-{n:03}" for n in range(1, 22)}, "access suite must retain its twenty-one stable contract cases"
    for case in access["cases"]:
        go_test = case.get("goTest")
        protected_test = case.get("protectedGoTest")
        assert bool(go_test) != bool(protected_test), f"access case must bind exactly one normal or protected Go test: {case['id']}"
        test_name = go_test or protected_test
        assert re.search(rf"func\s+{re.escape(test_name)}\s*\(", go_test_source), (
            f"access case references missing Go test: {case['id']}: {test_name}"
        )
        postgresql_test = case.get("postgresqlGoTest")
        if postgresql_test:
            assert re.search(rf"func\s+{re.escape(postgresql_test)}\s*\(", go_test_source), (
                f"access case references missing PostgreSQL test: {case['id']}: {postgresql_test}"
            )
    resilience = load_yaml(ROOT / "design/contracts/production-resilience.yaml")
    assert resilience["suite"] == "labcentral.production-resilience/v1alpha1"
    assert resilience["objectives"] == {
        "classroomSize": 100,
        "workerReplicas": 4,
        "admissionToReadyMaximumSeconds": 120,
        "destroyMaximumSeconds": 120,
    }
    assert len(resilience["cases"]) == 5, "production resilience suite must retain all five checkpoint cases"
    for case in resilience["cases"]:
        assert case.get("goTests"), f"production resilience case has no Go test bindings: {case['id']}"
        for go_test in case["goTests"]:
            assert re.search(rf"func\s+{re.escape(go_test)}\s*\(", go_test_source), (
                f"production resilience case references missing Go test: {case['id']}: {go_test}"
            )
    recovery = load_yaml(ROOT / "design/contracts/production-recovery.yaml")
    assert recovery["suite"] == "labcentral.production-recovery/v1alpha1"
    assert len(recovery["cases"]) == 6, "production recovery suite must retain all six checkpoint cases"
    for case in recovery["cases"]:
        bindings = case.get("goTest", "").split(" and ")
        assert all(bindings), f"production recovery case has no Go test bindings: {case['id']}"
        for go_test in bindings:
            assert re.search(rf"func\s+{re.escape(go_test)}\s*\(", go_test_source), (
                f"production recovery case references missing Go test: {case['id']}: {go_test}"
            )
    release = load_yaml(ROOT / "design/contracts/production-release.yaml")
    assert release["suite"] == "labcentral.production-release/v1alpha1"
    assert release["publisher"] == {
        "repository": "OutboundSpade/LabCentral",
        "workflow": ".github/workflows/publisher-release.yml",
        "environment": "publisher-release",
        "runner": "github-hosted",
        "oidcIssuer": "https://token.actions.githubusercontent.com",
        "certificateIdentityPrefix": "https://github.com/OutboundSpade/LabCentral/.github/workflows/publisher-release.yml@refs/tags/",
        "releaseRefPattern": "vMAJOR.MINOR.PATCH",
    }
    assert len(release["cases"]) == 7, "production release suite must retain all seven cases"
    for case in release["cases"]:
        for go_test in case.get("goTests", []):
            assert re.search(rf"func\s+{re.escape(go_test)}\s*\(", go_test_source), (
                f"production release case references missing Go test: {case['id']}: {go_test}"
            )
    topology = load_yaml(ROOT / "design/contracts/production-topology.yaml")
    assert topology["suite"] == "labcentral.production-topology/v1alpha1"
    assert len(topology["cases"]) == 8, "production topology suite must retain all eight checkpoint cases"
    for case in topology["cases"]:
        for field in ["binding", "startupOrderBinding", "healthBinding", "processBinding"]:
            binding = case.get(field)
            if not binding:
                continue
            relative_path, function_name = binding.split("#", 1)
            source = (ROOT / relative_path).read_text(encoding="utf-8")
            if relative_path.endswith(".go"):
                marker = f"func {function_name}("
            else:
                marker = f"def {function_name}("
            assert marker in source, f"production topology binding is missing: {case['id']}: {binding}"
    security = load_yaml(ROOT / "design/contracts/security-controls.yaml")
    threat_ids: set[str] = set()
    for threat in security["threats"]:
        assert threat["id"] not in threat_ids, f"duplicate threat ID: {threat['id']}"
        threat_ids.add(threat["id"])
        assert threat["owners"], f"threat has no owner: {threat['id']}"
        assert threat["controls"], f"threat has no control: {threat['id']}"
        assert threat["tests"], f"threat has no tests: {threat['id']}"
        missing = set(threat["tests"]) - tests
        assert not missing, f"threat references undefined tests: {threat['id']}: {sorted(missing)}"


def verify_kubernetes_topology() -> None:
    deployment_root = ROOT / "deploy/kubernetes"
    kustomization = load_yaml(deployment_root / "kustomization.yaml")
    resources = set(kustomization["resources"])
    assert resources == {
        "namespace.yaml",
        "service-account.yaml",
        "shared-storage.yaml",
        "services.yaml",
        "statefulset.yaml",
        "disruption-budget.yaml",
        "network-policies.yaml",
    }, "Kubernetes base resources changed without updating the production topology contract"
    assert "config.example.yaml" not in resources and "secrets.example.yaml" not in resources

    namespace = load_yaml(deployment_root / "namespace.yaml")
    labels = namespace["metadata"]["labels"]
    assert labels["pod-security.kubernetes.io/enforce"] == "restricted"
    assert labels["pod-security.kubernetes.io/audit"] == "restricted"
    assert labels["pod-security.kubernetes.io/warn"] == "restricted"

    service_account = load_yaml(deployment_root / "service-account.yaml")
    assert service_account["automountServiceAccountToken"] is False

    stateful_set = load_yaml(deployment_root / "statefulset.yaml")
    spec = stateful_set["spec"]
    assert spec["replicas"] == 2 and spec["updateStrategy"]["type"] == "RollingUpdate"
    pod = spec["template"]["spec"]
    assert pod["automountServiceAccountToken"] is False and pod["enableServiceLinks"] is False
    anti_affinity = pod["affinity"]["podAntiAffinity"]["requiredDuringSchedulingIgnoredDuringExecution"]
    assert anti_affinity and anti_affinity[0]["topologyKey"] == "kubernetes.io/hostname"
    assert not any("hostPath" in volume for volume in pod["volumes"])
    volumes = {volume["name"]: volume for volume in pod["volumes"]}
    for name in ["core-secrets", "proxmox-secrets", "proxmox-registration", "access-registration"]:
        assert volumes[name]["secret"]["defaultMode"] == 0o440

    containers = {container["name"]: container for container in pod["containers"]}
    assert set(containers) == {"labd", "proxmox-provider", "novnc-access"}
    expected_uids = {"labd": 10000, "proxmox-provider": 11000, "novnc-access": 12000}
    placeholder_digest = "sha256:" + "0" * 64
    for name, container in containers.items():
        security_context = container["securityContext"]
        assert security_context["runAsUser"] == expected_uids[name]
        assert security_context["runAsGroup"] == 20000
        assert security_context["allowPrivilegeEscalation"] is False
        assert security_context["readOnlyRootFilesystem"] is True
        assert security_context["capabilities"]["drop"] == ["ALL"]
        assert container["image"].endswith("@" + placeholder_digest)

    core_mounts = {mount["name"] for mount in containers["labd"]["volumeMounts"]}
    proxmox_mounts = {mount["name"] for mount in containers["proxmox-provider"]["volumeMounts"]}
    access_mounts = {mount["name"] for mount in containers["novnc-access"]["volumeMounts"]}
    assert "core-secrets" in core_mounts and "core-secrets" not in proxmox_mounts | access_mounts
    assert "proxmox-secrets" in core_mounts and "proxmox-secrets" not in proxmox_mounts | access_mounts
    assert "proxmox-registration" in core_mounts & proxmox_mounts and "proxmox-registration" not in access_mounts
    assert "access-registration" in core_mounts & access_mounts and "access-registration" not in proxmox_mounts
    assert "shared-state" in core_mounts & proxmox_mounts and "shared-state" not in access_mounts

    core_args = containers["labd"]["args"]
    assert containers["labd"]["startupProbe"]["failureThreshold"] >= 180
    assert "--deployment-mode=multi-replica" in core_args
    assert "--artifact-mode=shared-filesystem" in core_args
    assert "--artifact-dir=/var/lib/labcentral/shared/artifacts" in core_args
    assert "--allow-group-readable-secret-files" in core_args
    assert "--bootstrap-oidc-subject=$(BOOTSTRAP_OIDC_SUBJECT)" in core_args
    assert "--proxmox-plugin-runtime-dir=/run/labcentral/proxmox/ipc" in core_args
    assert "--access-plugin-runtime-dir=/run/labcentral/access/ipc" in core_args
    assert "--docker-plugin=" in core_args
    proxmox_args = containers["proxmox-provider"]["args"]
    assert "--initialize-journal-directory=/var/lib/labcentral/shared/proxmox-journal" in proxmox_args

    for name in ["proxmox-provider", "novnc-access"]:
        environment = {entry["name"]: entry.get("value") for entry in containers[name]["env"]}
        assert environment["LABCENTRAL_PLUGIN_SOCKET_MODE"] == "0660"
        assert environment["LABCENTRAL_PLUGIN_LAUNCH_TOKEN_FILE"]
        assert "LABCENTRAL_PLUGIN_LAUNCH_TOKEN" not in environment

    pdb = load_yaml(deployment_root / "disruption-budget.yaml")
    assert pdb["spec"]["minAvailable"] == 1
    assert pdb["spec"]["unhealthyPodEvictionPolicy"] == "AlwaysAllow"
    pvc = load_yaml(deployment_root / "shared-storage.yaml")
    assert pvc["spec"]["accessModes"] == ["ReadWriteMany"]

    policies = {document["metadata"]["name"]: document for document in load_yaml_documents(deployment_root / "network-policies.yaml")}
    deny = policies["labcentral-default-deny"]["spec"]
    assert set(deny["policyTypes"]) == {"Ingress", "Egress"}
    assert "ingress" not in deny and "egress" not in deny
    egress = policies["labcentral-allowed-egress"]["spec"]["egress"]
    cidrs = {
        target["ipBlock"]["cidr"]
        for rule in egress
        for target in rule.get("to", [])
        if "ipBlock" in target
    }
    assert cidrs == {"192.0.2.10/32", "192.0.2.20/32", "192.0.2.30/32"}

    example_config = load_yaml_documents(deployment_root / "config.example.yaml")
    proxmox_config = next(document for document in example_config if document["metadata"]["name"] == "labcentral-proxmox")
    assert "/var/lib/labcentral/shared/proxmox-journal" in proxmox_config["data"]["target.json"]


def systemd_values(path: Path, key: str) -> list[str]:
    prefix = key + "="
    return [
        line.strip().split("=", 1)[1]
        for line in path.read_text(encoding="utf-8").splitlines()
        if line.strip().startswith(prefix)
    ]


def verify_native_topology() -> None:
    deployment_root = ROOT / "deploy/proxmox"
    paths = {
        "core": deployment_root / "labcentral.service",
        "provider": deployment_root / "labcentral-proxmox-provider.service",
        "access": deployment_root / "labcentral-novnc.service",
    }
    for path in paths.values():
        assert path.is_file(), f"native service asset is missing: {path.name}"

    expected_identities = {
        "core": ("labcentral", "labcentral"),
        "provider": ("labcentral-proxmox", "labcentral-proxmox-ipc"),
        "access": ("labcentral-access", "labcentral-access-ipc"),
    }
    common_hardening = {
        "NoNewPrivileges": "yes",
        "PrivateDevices": "yes",
        "PrivateTmp": "yes",
        "ProtectProc": "invisible",
        "ProcSubset": "pid",
        "ProtectSystem": "strict",
        "RestrictNamespaces": "yes",
        "RestrictSUIDSGID": "yes",
        "SystemCallFilter": "@system-service",
        "SystemCallErrorNumber": "EPERM",
        "CapabilityBoundingSet": "",
        "AmbientCapabilities": "",
        "DevicePolicy": "closed",
        "IPAddressDeny": "any",
        "IPAddressAllow": "localhost",
    }
    for name, path in paths.items():
        expected_user, expected_group = expected_identities[name]
        assert systemd_values(path, "User") == [expected_user]
        assert systemd_values(path, "Group") == [expected_group]
        for key, expected in common_hardening.items():
            assert systemd_values(path, key) == [expected], f"{path.name} has unsafe or missing {key}"

    core_text = paths["core"].read_text(encoding="utf-8")
    supplementary = systemd_values(paths["core"], "SupplementaryGroups")
    assert supplementary == ["labcentral-proxmox-ipc labcentral-access-ipc"]
    assert "--proxmox-plugin-runtime-dir=/run/labcentral-proxmox/ipc" in core_text
    assert "--access-plugin-runtime-dir=/run/labcentral-access/ipc" in core_text
    assert "--proxmox-plugin=/opt" not in core_text and "--access-plugin=/opt" not in core_text
    assert set(systemd_values(paths["core"], "LoadCredential")) == {
        "proxmox-registration.token:/etc/labcentral/proxmox-registration.token",
        "access-registration.token:/etc/labcentral/access-registration.token",
    }
    assert "${CREDENTIALS_DIRECTORY}/proxmox-registration.token" in core_text
    assert "${CREDENTIALS_DIRECTORY}/access-registration.token" in core_text
    assert "--proxmox-credential-file=/etc/labcentral/proxmox-credential.json" in core_text

    provider_text = paths["provider"].read_text(encoding="utf-8")
    access_text = paths["access"].read_text(encoding="utf-8")
    assert systemd_values(paths["provider"], "RuntimeDirectory") == ["labcentral-proxmox labcentral-proxmox/ipc"]
    assert systemd_values(paths["provider"], "RuntimeDirectoryMode") == ["2770"]
    assert systemd_values(paths["provider"], "StateDirectory") == ["labcentral-proxmox"]
    assert systemd_values(paths["provider"], "StateDirectoryMode") == ["0700"]
    assert "--initialize-journal-directory=/var/lib/labcentral-proxmox/proxmox-journal" in provider_text
    assert systemd_values(paths["access"], "RuntimeDirectory") == ["labcentral-access labcentral-access/ipc"]
    assert systemd_values(paths["access"], "RuntimeDirectoryMode") == ["2770"]
    assert "InaccessiblePaths=/var/lib/labcentral" in provider_text
    assert "InaccessiblePaths=/var/lib/labcentral /var/lib/labcentral-proxmox" in access_text
    for path, credential in [
        (paths["provider"], "proxmox-registration.token"),
        (paths["access"], "access-registration.token"),
    ]:
        text = path.read_text(encoding="utf-8")
        assert f"LABCENTRAL_PLUGIN_LAUNCH_TOKEN_FILE=%d/{credential}" in text
        assert "LABCENTRAL_PLUGIN_LAUNCH_TOKEN=" not in text
        assert f"{credential}:/etc/labcentral/{credential}" in systemd_values(path, "LoadCredential")
        assert "LABCENTRAL_PLUGIN_SOCKET_MODE=0660" in text

    sysusers = (deployment_root / "labcentral.sysusers").read_text(encoding="utf-8")
    for identity in ["labcentral", "labcentral-proxmox", "labcentral-access"]:
        assert re.search(rf"^u\s+{re.escape(identity)}\s", sysusers, re.MULTILINE)
    for membership in [
        r"^m\s+labcentral\s+labcentral-proxmox-ipc$",
        r"^m\s+labcentral-proxmox\s+labcentral-proxmox-ipc$",
        r"^m\s+labcentral\s+labcentral-access-ipc$",
        r"^m\s+labcentral-access\s+labcentral-access-ipc$",
    ]:
        assert re.search(membership, sysusers, re.MULTILINE)
    assert not re.search(r"^m\s+labcentral-access\s+labcentral-proxmox-ipc$", sysusers, re.MULTILINE)
    assert not re.search(r"^m\s+labcentral-proxmox\s+labcentral-access-ipc$", sysusers, re.MULTILINE)

    network_dropin = (deployment_root / "provider-network.example.conf").read_text(encoding="utf-8")
    assert "IPAddressAllow=192.0.2.20/32" in network_dropin
    target = json.loads((deployment_root / "proxmox-target.json.example").read_text(encoding="utf-8"))
    assert target["journalDirectory"] == "/var/lib/labcentral-proxmox/proxmox-journal"

    release_source = (ROOT / "tools/release/main.go").read_text(encoding="utf-8")
    for packaged in [
        "labcentral-proxmox-provider.service",
        "labcentral-novnc.service",
        "labcentral.sysusers",
        "provider-network.example.conf",
    ]:
        assert packaged in release_source, f"native asset is not packaged: {packaged}"


def verify_release_supply_chain() -> None:
    ci_path = ROOT / ".github/workflows/ci.yml"
    publisher_path = ROOT / ".github/workflows/publisher-release.yml"
    ci = load_yaml(ci_path)
    publisher = load_yaml(publisher_path)
    ci_text = ci_path.read_text(encoding="utf-8")
    publisher_text = publisher_path.read_text(encoding="utf-8")

    web_audit_path = ROOT / "tools/audit_web_dependencies.sh"
    web_audit = web_audit_path.read_text(encoding="utf-8")
    assert web_audit_path.stat().st_mode & 0o111, "web audit gate must be executable"
    assert "timeout 300s osv-scanner scan source --lockfile=bun.lock" in web_audit
    assert 'while [ "$attempt" -le 2 ]' in web_audit

    assert ci["permissions"] == {"contents": "read"}, "normal CI must grant only repository-content read access"
    assert publisher["permissions"] == {}, "publisher must default to no GitHub token permissions"
    publish_job = publisher["jobs"]["publish"]
    assert publish_job["environment"] == "publisher-release"
    assert publish_job["runs-on"] == "ubuntu-24.04"
    assert publish_job["permissions"] == {
        "attestations": "write",
        "contents": "write",
        "id-token": "write",
        "packages": "write",
    }
    assert "github.repository == 'OutboundSpade/LabCentral'" in publish_job["if"]

    for path, text_value in [(ci_path, ci_text), (publisher_path, publisher_text)]:
        actions = re.findall(r"^\s*uses:\s*([^\s#]+)", text_value, re.MULTILINE)
        assert actions, f"workflow has no pinned actions: {path}"
        for action in actions:
            assert re.fullmatch(r"[^@\s]+@[0-9a-f]{40}", action), f"workflow action is not full-SHA pinned: {path}: {action}"

    for text_value in [ci_text, publisher_text]:
        assert "postgres:18@sha256:" in text_value
        assert "go test -count=1 ./..." in text_value
        assert "go test -race -count=1 ./..." in text_value
        assert "go vet ./..." in text_value
        assert "timeout 300s govulncheck ./..." in text_value
        assert "github.com/google/osv-scanner/v2/cmd/osv-scanner@v2.5.0" in text_value
        assert "tools/audit_web_dependencies.sh" in text_value
        assert "python3 tools/verify_phase0.py" in text_value

    required_publisher_fragments = [
        "git rev-parse \"$GITHUB_REF_NAME^{commit}\"",
        "git merge-base --is-ancestor \"$GITHUB_SHA\" refs/remotes/origin/main",
        "test -z \"$(git status --porcelain=v1)\"",
        "SOURCE_DATE_EPOCH=\"$epoch\" go run ./tools/release",
        'cmp "dist/first/$asset" "dist/second/$asset"',
        'sha256sum -c "$asset.sha256"',
        "sha256sum -c SHA256SUMS",
        "cosign sign-blob --yes --bundle",
        "cosign verify-blob",
        "--certificate-identity \"$identity\"",
        "--certificate-oidc-issuer \"$SIGSTORE_OIDC_ISSUER\"",
        "cosign sign --yes \"$IMAGE_REFERENCE\"",
        "cosign verify",
        "subject-path: ${{ steps.native.outputs.path }}",
        "subject-digest: ${{ steps.image.outputs.digest }}",
        "push-to-registry: true",
        "gh release create \"$GITHUB_REF_NAME\"",
        "--verify-tag",
    ]
    for fragment in required_publisher_fragments:
        assert fragment in publisher_text, f"publisher gate is missing: {fragment}"
    assert publisher_text.index("cosign verify-blob") < publisher_text.index("gh release create")
    assert publisher_text.index("cosign verify \\") < publisher_text.index("gh release create")
    assert publisher_text.count("go run ./tools/release") == 2

    identity = "https://github.com/OutboundSpade/LabCentral/.github/workflows/publisher-release.yml@refs/tags/"
    assert identity in publisher_text
    assert "https://token.actions.githubusercontent.com" in publisher_text
    release_source = (ROOT / "tools/release/main.go").read_text(encoding="utf-8")
    for packaged in [
        "docs/operations/release-verification.md",
        "docs/compatibility.md",
    ]:
        assert packaged in release_source, f"release guidance is not packaged: {packaged}"
    release_guide = (ROOT / "docs/operations/release-verification.md").read_text(encoding="utf-8")
    assert "--cert-identity \"$IDENTITY\"" in release_guide
    assert "--source-ref \"refs/tags/${VERSION}\"" in release_guide
    server_source = "\n".join(path.read_text(encoding="utf-8") for path in (ROOT / "api/http").glob("*.go") if not path.name.endswith("_test.go"))
    assert 'ServerVersion: options.ServerVersion' in server_source
    assert 'ServerVersion: "0.1.0"' not in server_source


def verify_markdown_links_and_state() -> None:
    link_pattern = re.compile(r"\[[^\]]*\]\(([^)]+)\)")
    for markdown in ROOT.rglob("*.md"):
        # Generated dependency/build trees are not repository documentation and
        # may contain package-relative links whose source files are not shipped.
        if {".git", "node_modules", ".svelte-kit"}.intersection(markdown.relative_to(ROOT).parts):
            continue
        for raw_target in link_pattern.findall(markdown.read_text(encoding="utf-8")):
            target = raw_target.split("#", 1)[0]
            if not target or "://" in target or target.startswith("mailto:"):
                continue
            assert (markdown.parent / target).resolve().exists(), f"broken link: {markdown}: {raw_target}"

    state = load_yaml(ROOT / "design/state.yaml")
    assert len(state["openQuestions"]) <= 5, "more than five open questions"
    for document in state["documents"]:
        assert (ROOT / document["path"]).exists(), f"state references missing path: {document['path']}"
    checklist = load_yaml(ROOT / "design/checklist.yaml")
    tasks = {task["id"]: task for task in checklist["tasks"]}
    task_ids = [task["id"] for task in checklist["tasks"]]
    assert len(task_ids) == len(set(task_ids)), "duplicate checklist task ID"
    completed = set(state["completedTasks"])
    current = set(state["currentTasks"])
    next_recommended = set(state["nextRecommendedTasks"])
    assert completed.isdisjoint(current), "task is both completed and current"
    for group_name, task_group in {
        "completedTasks": completed,
        "currentTasks": current,
        "nextRecommendedTasks": next_recommended,
    }.items():
        missing = task_group - tasks.keys()
        assert not missing, f"state {group_name} references missing checklist tasks: {sorted(missing)}"
    for task_id in completed:
        assert tasks[task_id]["status"] == "complete", f"completed task status disagrees with checklist: {task_id}"
    for task_id in current:
        assert tasks[task_id]["status"] == "in_progress", f"current task status disagrees with checklist: {task_id}"
    for task in tasks.values():
        missing = set(task["dependencies"]) - tasks.keys()
        assert not missing, f"task references missing dependencies: {task['id']}: {sorted(missing)}"
        if task["status"] == "complete":
            incomplete = set(task["dependencies"]) - completed
            assert not incomplete, f"completed task has incomplete dependencies: {task['id']}: {sorted(incomplete)}"


def verify_architecture_contracts() -> None:
    contract = load_yaml(ROOT / "design/contracts/architecture-conformance.yaml")
    ids = set()
    for case in contract["cases"]:
        assert case["id"] not in ids, "duplicate architecture test ID"
        ids.add(case["id"])
        source = (ROOT / case["file"]).read_text(encoding="utf-8")
        assert "func " + case["test"] + "(" in source, f"missing bound architecture test: {case['id']}"
    state = load_yaml(ROOT / "design/state.yaml")
    for status, key in [("Accepted", "acceptedAdrs"), ("Superseded", "supersededAdrs")]:
        for identity in state[key]:
            paths = list((ROOT / "docs/adr").glob(identity[4:] + "-*.md"))
            assert len(paths) == 1 and "**Status:** " + status + "\n" in paths[0].read_text(), f"ADR state drift: {identity}"
    schema = json.loads((ROOT / "api/http/openapi.json").read_text())
    assert "InputValue" in schema["components"]["schemas"]
    assert "openapi-typescript" in (ROOT / "web/package.json").read_text()


def main() -> int:
    bun = executable("BUN", "bun")
    buf = executable("BUFBIN", "buf")
    checks = [
        ("YAML and JSON syntax", verify_yaml_and_json),
        ("bundle manifests", verify_bundles),
        ("Lab schema and parser fixtures", verify_lab_schema),
        ("canonical golden vectors", lambda: verify_canonical_vectors(bun)),
        ("plugin behavior fixtures", verify_plugin_cases),
        ("Protobuf format/lint/breaking baseline", lambda: verify_proto(buf)),
        ("provider/job/security contract catalogs", verify_contract_catalogs),
        ("Proxmox QEMU mutation design contract", verify_proxmox_mutation_contract),
        ("Kubernetes production topology", verify_kubernetes_topology),
        ("native production topology", verify_native_topology),
        ("production release supply chain", verify_release_supply_chain),
        ("architecture contracts and decision state", verify_architecture_contracts),
        ("documentation links and project state", verify_markdown_links_and_state),
    ]
    for label, check in checks:
        check()
        print(f"PASS: {label}")
    print("PASS: LabCentral design contracts are Phase 0 implementation-ready")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (AssertionError, DuplicateKeyError, json.JSONDecodeError, yaml.YAMLError) as error:
        print(f"FAIL: {error}", file=sys.stderr)
        raise SystemExit(1)
