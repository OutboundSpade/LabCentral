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
        assert current["protocol"] == {"major": 1, "minor": 1}, "current plugin protocol version is not 1.1"
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
    assert contract["status"] == "t0_implemented", "mutation contract must retain its implemented T0 gate"
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
    assert qualification["status"] == "testbed_signed_and_verified_ready_for_independent_review"
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
    assert qualification["productionEnablement"] == "separate_accepted_adr_and_implementation_required"
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
        "status": "implemented_executed_once",
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
        "status": "implemented_executed_evidence_grade",
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
        path.read_text(encoding="utf-8") for path in ROOT.glob("internal/**/*_test.go")
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
