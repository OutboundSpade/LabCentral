package render

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func TestRendererRendersExamplesDeterministically(t *testing.T) {
	t.Parallel()
	renderer := testRenderer(t, DefaultLimits())
	tests := []struct {
		name      string
		resources int
		ids       []domain.LogicalResourceID
	}{
		{"docker-laptop", 6, []domain.LogicalResourceID{"network.app", "volume.data", "container.client", "container.web", "access.client-shell", "output.web-url"}},
		{"portable-containers", 6, []domain.LogicalResourceID{"network.service", "container.api", "container.collector", "container.loadgen[one]", "container.loadgen[two]", "access.api-shell"}},
		{"routed-cyber-lab", 7, []domain.LogicalResourceID{"network.blue", "network.red", "vm.attacker", "vm.router", "vm.target", "access.attacker-console", "output.target-ip"}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			input, err := os.ReadFile(filepath.Join(repositoryRoot(t), "examples", testCase.name, "lab.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			revision := domain.RevisionContent{VerifiedBundle: input}
			first, report, err := renderer.Render(context.Background(), revision, nil, domain.RenderPolicy{})
			if err != nil {
				t.Fatalf("first render: %v", err)
			}
			second, _, err := renderer.Render(context.Background(), revision, nil, domain.RenderPolicy{})
			if err != nil {
				t.Fatalf("second render: %v", err)
			}
			if !bytes.Equal(first.CanonicalBytes, second.CanonicalBytes) || first.Digest != second.Digest {
				t.Fatal("render was not deterministic")
			}
			if len(first.Resources) != testCase.resources {
				t.Fatalf("resource count = %d, want %d", len(first.Resources), testCase.resources)
			}
			ids := make([]domain.LogicalResourceID, len(first.Resources))
			for index := range first.Resources {
				ids[index] = first.Resources[index].ID()
			}
			if !reflect.DeepEqual(ids, testCase.ids) {
				t.Fatalf("resource IDs = %v, want %v", ids, testCase.ids)
			}
			if len(report.Warnings) != 0 {
				t.Fatalf("unexpected render warnings: %v", report.Warnings)
			}
			if testCase.name == "docker-laptop" {
				expected, err := os.ReadFile(filepath.Join(repositoryRoot(t), "examples", "docker-laptop", "resolved.json"))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(first.CanonicalBytes, bytes.TrimSpace(expected)) {
					t.Fatalf("Docker canonical fixture drifted\n got: %s\nwant: %s", first.CanonicalBytes, bytes.TrimSpace(expected))
				}
			}
		})
	}
}

func TestRenderContract001UnresolvedTypedReference(t *testing.T) {
	t.Parallel()
	renderer := testRenderer(t, DefaultLimits())
	input := []byte(`apiVersion: labs.labcentral.io/v1alpha1
kind: Lab
metadata: {name: unresolved-reference}
spec:
  resources:
    containers:
      - name: worker
        image: example.invalid/worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
        compute: {cpu: 1, memory: 1Gi, architecture: amd64}
        isolation: process
        interfaces: [{networkRef: networks.missing}]
`)
	_, _, err := renderer.Render(context.Background(), domain.RevisionContent{VerifiedBundle: input}, nil, domain.RenderPolicy{})
	var renderError *Error
	if !errors.As(err, &renderError) || renderError.Code != "unresolved_reference" || renderError.Stage != StageReferenceResolution {
		t.Fatalf("unexpected unresolved reference error: %v", err)
	}
}

func TestRenderContract006ConditionalResourceAndAttachmentAgree(t *testing.T) {
	t.Parallel()
	renderer := testRenderer(t, DefaultLimits())
	input, err := os.ReadFile(filepath.Join(repositoryRoot(t), "examples", "routed-cyber-lab", "lab.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	canonical, _, err := renderer.Render(context.Background(), domain.RevisionContent{VerifiedBundle: input}, nil, domain.RenderPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range canonical.Resources {
		if resource.ID() == "network.uplink" {
			t.Fatal("conditionally false network was retained")
		}
		if resource.ID() == "vm.router" {
			router := resource.(domain.VM)
			if len(router.Spec.Interfaces) != 2 {
				t.Fatalf("router interfaces = %d, want 2", len(router.Spec.Interfaces))
			}
		}
	}
}

func TestRenderContract007ConditionalAttachmentReferencesOmittedResource(t *testing.T) {
	t.Parallel()
	renderer := testRenderer(t, DefaultLimits())
	input := []byte(`apiVersion: labs.labcentral.io/v1alpha1
kind: Lab
metadata: {name: conditional-reference}
spec:
  parameters:
    - {name: enabled, type: boolean, default: false}
  resources:
    networks:
      - name: uplink
        kind: external
        ipam: providerManaged
        egress: {mode: deny}
        when: params.enabled
    vms:
      - name: router
        imageRef: targetImage:router
        osFamily: linux
        compute: {cpu: 1, memory: 1Gi, architecture: amd64}
        interfaces: [{networkRef: networks.uplink}]
`)
	_, _, err := renderer.Render(context.Background(), domain.RevisionContent{VerifiedBundle: input}, nil, domain.RenderPolicy{})
	var renderError *Error
	if !errors.As(err, &renderError) || renderError.Code != "unresolved_reference_after_expansion" {
		t.Fatalf("unexpected conditional reference error: %v", err)
	}
}

func TestRenderContract008ExpansionLimitFailsAtomically(t *testing.T) {
	t.Parallel()
	limits := DefaultLimits()
	limits.MaxExpandedResources = 10
	renderer := testRenderer(t, limits)
	input := []byte(`apiVersion: labs.labcentral.io/v1alpha1
kind: Lab
metadata: {name: expansion-limit}
spec:
  parameters:
    - {name: ids, type: stringList, required: true}
  resources:
    containers:
      - name: worker
        forEach: {key: id, in: params.ids}
        image: example.invalid/worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
        compute: {cpu: 1, memory: 1Gi, architecture: amd64}
        isolation: process
`)
	ids := make([]string, 11)
	for index := range ids {
		ids[index] = string(rune('a' + index))
	}
	canonical, _, err := renderer.Render(context.Background(), domain.RevisionContent{VerifiedBundle: input}, domain.TypedInputs{{
		Name:       "ids",
		Kind:       domain.InputStringList,
		StringList: ids,
	}}, domain.RenderPolicy{})
	var renderError *Error
	if !errors.As(err, &renderError) || renderError.Code != "expansion_limit_exceeded" {
		t.Fatalf("unexpected expansion error: %v", err)
	}
	if len(canonical.CanonicalBytes) != 0 || len(canonical.Resources) != 0 {
		t.Fatal("expansion failure returned partial canonical output")
	}
}

func TestRendererDoesNotExposeSecretInputToCELOrCanonicalBytes(t *testing.T) {
	t.Parallel()
	renderer := testRenderer(t, DefaultLimits())
	input := []byte(`apiVersion: labs.labcentral.io/v1alpha1
kind: Lab
metadata: {name: secret-expression}
spec:
  parameters:
    - {name: credential, type: secretRef, required: true}
  resources:
    networks:
      - name: private
        kind: isolated
        ipam: providerManaged
        egress: {mode: deny}
        when: params.credential
`)
	_, _, err := renderer.Render(context.Background(), domain.RevisionContent{VerifiedBundle: input}, domain.TypedInputs{{
		Name:      "credential",
		Kind:      domain.InputSecretRef,
		SecretRef: &domain.SecretRef{Source: "vault", Name: "lab", Key: "password"},
	}}, domain.RenderPolicy{})
	var renderError *Error
	if !errors.As(err, &renderError) || renderError.Code != "secret_value_unavailable" {
		t.Fatalf("unexpected secret expression error: %v", err)
	}
}

func TestRendererExpandsLexicalComponentsWithStableQualifiedNames(t *testing.T) {
	t.Parallel()
	renderer := testRenderer(t, DefaultLimits())
	input := []byte(`apiVersion: labs.labcentral.io/v1alpha1
kind: Lab
metadata: {name: component-lab}
spec:
  parameters:
    - {name: copies, type: stringList, default: [two, one]}
    - {name: enabled, type: boolean, default: true}
  components:
    - name: service
      parameters:
        - {name: enabled, type: boolean, required: true}
      resources:
        networks:
          - {name: private, kind: isolated, ipam: providerManaged, egress: {mode: deny}, when: params.enabled}
        containers:
          - name: app
            image: example.invalid/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
            compute: {cpu: 1, memory: 1Gi, architecture: amd64}
            isolation: process
            interfaces: [{networkRef: networks.private}]
            when: params.enabled
        accessEndpoints:
          - {name: shell, targetRef: containers.app, methods: [containerExec], when: params.enabled}
  uses:
    - {name: primary, componentRef: service, arguments: {enabled: params.enabled}}
    - name: replicas
      componentRef: service
      arguments: {enabled: true}
      forEach: {key: copy, in: params.copies}
    - {name: omitted, componentRef: service, arguments: {enabled: false}}
  resources: {}
`)
	canonical, _, err := renderer.Render(context.Background(), domain.RevisionContent{VerifiedBundle: input}, nil, domain.RenderPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]domain.LogicalResourceID, len(canonical.Resources))
	for index := range canonical.Resources {
		ids[index] = canonical.Resources[index].ID()
	}
	want := []domain.LogicalResourceID{
		"network.primary-private",
		"network.replicas-one-private",
		"network.replicas-two-private",
		"container.primary-app",
		"container.replicas-one-app",
		"container.replicas-two-app",
		"access.primary-shell",
		"access.replicas-one-shell",
		"access.replicas-two-shell",
	}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("component resource IDs = %v, want %v", ids, want)
	}
}

func TestRendererPreservesComponentIterationBindingsInAttachmentConditions(t *testing.T) {
	t.Parallel()
	renderer := testRenderer(t, DefaultLimits())
	input := []byte(`apiVersion: labs.labcentral.io/v1alpha1
kind: Lab
metadata: {name: component-iteration-scope}
spec:
  parameters:
    - {name: copies, type: stringList, default: [one]}
  components:
    - name: service
      resources:
        networks:
          - {name: private, kind: isolated, ipam: providerManaged, egress: {mode: deny}}
        containers:
          - name: app
            image: example.invalid/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
            compute: {cpu: 1, memory: 1Gi, architecture: amd64}
            isolation: process
            interfaces:
              - {networkRef: networks.private, when: copy == "one"}
  uses:
    - name: replicas
      componentRef: service
      forEach: {key: copy, in: params.copies}
  resources: {}
`)
	canonical, _, err := renderer.Render(context.Background(), domain.RevisionContent{VerifiedBundle: input}, nil, domain.RenderPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range canonical.Resources {
		if resource.ID() == "container.replicas-one-app" {
			container := resource.(domain.Container)
			if len(container.Spec.Interfaces) != 1 {
				t.Fatalf("component container interfaces = %d, want 1", len(container.Spec.Interfaces))
			}
			return
		}
	}
	t.Fatal("expanded component container was not rendered")
}

func TestRendererRejectsMissingComponent(t *testing.T) {
	t.Parallel()
	renderer := testRenderer(t, DefaultLimits())
	input := []byte(`apiVersion: labs.labcentral.io/v1alpha1
kind: Lab
metadata: {name: missing-component}
spec:
  uses: [{name: missing, componentRef: absent}]
  resources: {}
`)
	_, _, err := renderer.Render(context.Background(), domain.RevisionContent{VerifiedBundle: input}, nil, domain.RenderPolicy{})
	var renderError *Error
	if !errors.As(err, &renderError) || renderError.Code != "component_not_found" {
		t.Fatalf("unexpected missing component error: %v", err)
	}
}

func TestRendererEnforcesTypedParameterConstraints(t *testing.T) {
	t.Parallel()
	renderer := testRenderer(t, DefaultLimits())
	input := []byte(`apiVersion: labs.labcentral.io/v1alpha1
kind: Lab
metadata: {name: constrained-inputs}
spec:
  parameters:
    - {name: replicas, type: integer, min: 1, max: 3, required: true}
    - {name: tier, type: enum, enum: [1, standard], required: true}
    - {name: label, type: string, pattern: '^[a-z]+$', required: true}
  resources: {}
`)
	standard := "standard"
	valid := domain.TypedInputs{
		{Name: "replicas", Kind: domain.InputInteger, Integer: 2},
		{Name: "tier", Kind: domain.InputEnum, Enum: &domain.EnumValue{String: &standard}},
		{Name: "label", Kind: domain.InputString, String: "valid"},
	}
	if _, _, err := renderer.Render(context.Background(), domain.RevisionContent{VerifiedBundle: input}, valid, domain.RenderPolicy{}); err != nil {
		t.Fatalf("valid constrained inputs: %v", err)
	}
	invalid := append(domain.TypedInputs(nil), valid...)
	invalid[0].Integer = 4
	_, _, err := renderer.Render(context.Background(), domain.RevisionContent{VerifiedBundle: input}, invalid, domain.RenderPolicy{})
	var renderError *Error
	if !errors.As(err, &renderError) || renderError.Code != "input_constraint_violation" {
		t.Fatalf("unexpected constraint error: %v", err)
	}
}

func TestRendererCanonicalIdentityIncludesResolvedNonSecretInputs(t *testing.T) {
	t.Parallel()
	renderer := testRenderer(t, DefaultLimits())
	input := []byte(`apiVersion: labs.labcentral.io/v1alpha1
kind: Lab
metadata: {name: input-identity}
spec:
  parameters:
    - {name: enabled, type: boolean, required: true}
  resources: {}
`)
	disabled, _, err := renderer.Render(context.Background(), domain.RevisionContent{VerifiedBundle: input}, domain.TypedInputs{{Name: "enabled", Kind: domain.InputBoolean, Boolean: false}}, domain.RenderPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	enabled, _, err := renderer.Render(context.Background(), domain.RevisionContent{VerifiedBundle: input}, domain.TypedInputs{{Name: "enabled", Kind: domain.InputBoolean, Boolean: true}}, domain.RenderPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Digest == enabled.Digest {
		t.Fatal("different materialized inputs produced the same canonical digest")
	}
	if !bytes.Contains(disabled.CanonicalBytes, []byte(`"inputs":{"enabled":false}`)) {
		t.Fatalf("canonical bytes omit resolved input: %s", disabled.CanonicalBytes)
	}
}

func TestRendererRejectsProviderOverrideNamespaceMismatch(t *testing.T) {
	t.Parallel()
	renderer := testRenderer(t, DefaultLimits())
	input := []byte(`apiVersion: labs.labcentral.io/v1alpha1
kind: Lab
metadata: {name: mismatched-override}
spec:
  providerOverrides:
    proxmox:
      apiVersion: provider.labcentral.io/libvirt/v1alpha1
      spec: {}
  resources: {}
`)
	_, _, err := renderer.Render(context.Background(), domain.RevisionContent{VerifiedBundle: input}, nil, domain.RenderPolicy{})
	var renderError *Error
	if !errors.As(err, &renderError) || renderError.Code != "provider_override_namespace_mismatch" {
		t.Fatalf("unexpected provider override error: %v", err)
	}
}

func TestRendererNormalizesRequiredCapabilities(t *testing.T) {
	t.Parallel()
	renderer := testRenderer(t, DefaultLimits())
	input := []byte(`apiVersion: labs.labcentral.io/v1alpha1
kind: Lab
metadata: {name: capability-order}
spec:
  requirements:
    capabilities: [workload.vm, network.segment.isolated]
  resources:
    networks:
      - {name: private, kind: isolated, ipam: providerManaged, egress: {mode: deny}}
`)
	canonical, _, err := renderer.Render(context.Background(), domain.RevisionContent{VerifiedBundle: input}, nil, domain.RenderPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	got := canonical.Resources[0].RequiredCapabilities().AllOf
	want := []domain.CapabilityID{"network.segment.isolated", "workload.vm"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("required capabilities = %v, want %v", got, want)
	}
}

func testRenderer(t *testing.T, limits Limits) *Renderer {
	t.Helper()
	schema, err := os.ReadFile(filepath.Join(repositoryRoot(t), "schemas", "lab-v1alpha1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := NewRenderer(schema, limits)
	if err != nil {
		t.Fatal(err)
	}
	return renderer
}
