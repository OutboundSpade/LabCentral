package render

import (
	"context"
	"fmt"
	"math"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/OutboundSpade/LabCentral/internal/domain"
)

const (
	CanonicalFormatVersion = "canonical.labcentral.io/v1alpha1"
	RendererVersion        = "labcentral.renderer/v1alpha1"
)

type Limits struct {
	Parse                ParseLimits
	Expression           ExpressionLimits
	MaxExpandedResources int
	MaxCanonicalBytes    int
}

func DefaultLimits() Limits {
	return Limits{
		Parse:                DefaultParseLimits(),
		Expression:           DefaultExpressionLimits(),
		MaxExpandedResources: 10_000,
		MaxCanonicalBytes:    8 << 20,
	}
}

type Renderer struct {
	schema *SchemaValidator
	limits Limits
}

type expansionContext struct {
	key       string
	variables map[string]any
}

type pendingResource struct {
	id           domain.LogicalResourceID
	dependencies []domain.LogicalResourceID
	required     domain.CapabilityExpression
	spec         domain.ResourceSpec
}

func NewRenderer(schemaBytes []byte, limits Limits) (*Renderer, error) {
	if limits.MaxExpandedResources <= 0 || limits.MaxCanonicalBytes <= 0 {
		return nil, renderError(StageExpansion, "invalid_render_limits", "", "render limits must be positive", nil)
	}
	if err := validateParseLimits(limits.Parse); err != nil {
		return nil, err
	}
	if _, err := NewExpressionEngine(limits.Expression); err != nil {
		return nil, err
	}
	validator, err := NewSchemaValidator(schemaBytes)
	if err != nil {
		return nil, err
	}
	return &Renderer{schema: validator, limits: limits}, nil
}

func (r *Renderer) Render(
	ctx context.Context,
	revision domain.RevisionContent,
	provided domain.TypedInputs,
	policy domain.RenderPolicy,
) (domain.CanonicalLab, domain.RenderReport, error) {
	limits := r.effectiveLimits(policy)
	engine, err := NewExpressionEngine(limits.Expression)
	if err != nil {
		return domain.CanonicalLab{}, domain.RenderReport{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.CanonicalLab{}, domain.RenderReport{}, renderError(StageParse, "render_canceled", "", "render was canceled", err)
	}
	document, err := ParseYAML(revision.VerifiedBundle, limits.Parse)
	if err != nil {
		return domain.CanonicalLab{}, domain.RenderReport{}, err
	}
	if err := r.schema.Validate(document); err != nil {
		return domain.CanonicalLab{}, domain.RenderReport{}, err
	}
	authorLab, err := decodeAuthorLab(document)
	if err != nil {
		return domain.CanonicalLab{}, domain.RenderReport{}, err
	}
	if err := validateProviderOverrides(authorLab.Spec.ProviderOverrides); err != nil {
		return domain.CanonicalLab{}, domain.RenderReport{}, err
	}
	inputs, err := resolveInputs(authorLab.Spec.Parameters, provided)
	if err != nil {
		return domain.CanonicalLab{}, domain.RenderReport{}, err
	}
	pending, report, err := r.expandResources(ctx, authorLab, inputs, engine, limits.MaxExpandedResources)
	if err != nil {
		return domain.CanonicalLab{}, domain.RenderReport{}, err
	}
	componentResources, componentReport, err := r.expandComponents(ctx, authorLab, inputs, engine, limits.MaxExpandedResources-len(pending))
	if err != nil {
		return domain.CanonicalLab{}, domain.RenderReport{}, err
	}
	pending = append(pending, componentResources...)
	report.Warnings = append(report.Warnings, componentReport.Warnings...)
	resources, err := finalizeResources(pending)
	if err != nil {
		return domain.CanonicalLab{}, domain.RenderReport{}, err
	}
	graph, err := BuildResourceGraph(resources)
	if err != nil {
		return domain.CanonicalLab{}, domain.RenderReport{}, err
	}
	canonicalValue, err := canonicalLabValue(authorLab, graph, inputs)
	if err != nil {
		return domain.CanonicalLab{}, domain.RenderReport{}, err
	}
	canonical, err := Canonicalize(canonicalValue)
	if err != nil {
		return domain.CanonicalLab{}, domain.RenderReport{}, err
	}
	if len(canonical) > limits.MaxCanonicalBytes {
		return domain.CanonicalLab{}, domain.RenderReport{}, renderError(StageCanonicalization, "canonical_size_limit_exceeded", "", "canonical Lab exceeds the configured byte limit", nil)
	}
	digest, err := CanonicalDigest(CanonicalFormatVersion, canonical)
	if err != nil {
		return domain.CanonicalLab{}, domain.RenderReport{}, err
	}
	return domain.CanonicalLab{
		APIVersion:      CanonicalFormatVersion,
		RendererVersion: RendererVersion,
		CanonicalBytes:  canonical,
		Digest:          digest,
		Resources:       graph.Resources,
	}, report, nil
}

func (r *Renderer) effectiveLimits(policy domain.RenderPolicy) Limits {
	limits := r.limits
	if policy.MaxBundleBytes > 0 && uint64(limits.Parse.MaxBytes) > policy.MaxBundleBytes {
		limits.Parse.MaxBytes = int(policy.MaxBundleBytes)
	}
	if policy.MaxResources > 0 && uint32(limits.MaxExpandedResources) > policy.MaxResources {
		limits.MaxExpandedResources = int(policy.MaxResources)
	}
	if policy.MaxCanonicalBytes > 0 && uint64(limits.MaxCanonicalBytes) > policy.MaxCanonicalBytes {
		limits.MaxCanonicalBytes = int(policy.MaxCanonicalBytes)
	}
	if policy.MaxExpressionCost > 0 && limits.Expression.MaxCost > policy.MaxExpressionCost {
		limits.Expression.MaxCost = policy.MaxExpressionCost
	}
	if limits.Expression.MaxCollectionItems > limits.MaxExpandedResources {
		limits.Expression.MaxCollectionItems = limits.MaxExpandedResources
	}
	return limits
}

func (r *Renderer) expandResources(
	ctx context.Context,
	lab AuthorLab,
	inputs resolvedInputs,
	engine *ExpressionEngine,
	maxResources int,
) ([]pendingResource, domain.RenderReport, error) {
	if err := validateAuthoredReferences(lab.Spec.Resources, lab.Spec.Outputs); err != nil {
		return nil, domain.RenderReport{}, err
	}
	required, err := capabilityExpression(lab.Spec.Requirements.Capabilities)
	if err != nil {
		return nil, domain.RenderReport{}, err
	}
	variables := ExpressionVariables{Params: inputs.params, SecretParameters: inputs.secretNames, Iteration: inputs.iteration}
	pending := make([]pendingResource, 0)
	report := domain.RenderReport{}
	appendResource := func(resource pendingResource) error {
		if len(pending) >= maxResources {
			return renderError(StageExpansion, "expansion_limit_exceeded", "", "resource expansion exceeds the configured limit", nil)
		}
		pending = append(pending, resource)
		return nil
	}

	for _, author := range lab.Spec.Resources.Networks {
		contexts, err := expandCommon(ctx, engine, author.AuthorCommon, variables)
		if err != nil {
			return nil, report, err
		}
		for _, expansion := range contexts {
			spec, err := networkSpec(author)
			if err != nil {
				return nil, report, resourceConversionError("networks."+author.Name, err)
			}
			id, err := expandedResourceID("network", author.Name, expansion.key)
			if err != nil {
				return nil, report, err
			}
			dependencies, err := resolveDeclaredDependencies(author.DependsOn)
			if err != nil {
				return nil, report, err
			}
			if err := appendResource(pendingResource{id: id, dependencies: dependencies, required: required, spec: domain.ResourceSpec{Network: &spec}}); err != nil {
				return nil, report, err
			}
		}
	}

	for _, author := range lab.Spec.Resources.Volumes {
		contexts, err := expandCommon(ctx, engine, author.AuthorCommon, variables)
		if err != nil {
			return nil, report, err
		}
		for _, expansion := range contexts {
			spec, err := volumeSpec(author)
			if err != nil {
				return nil, report, resourceConversionError("volumes."+author.Name, err)
			}
			id, err := expandedResourceID("volume", author.Name, expansion.key)
			if err != nil {
				return nil, report, err
			}
			dependencies, err := resolveDeclaredDependencies(author.DependsOn)
			if err != nil {
				return nil, report, err
			}
			if err := appendResource(pendingResource{id: id, dependencies: dependencies, required: required, spec: domain.ResourceSpec{Volume: &spec}}); err != nil {
				return nil, report, err
			}
		}
	}

	for _, author := range lab.Spec.Resources.VMs {
		contexts, err := expandCommon(ctx, engine, author.AuthorCommon, variables)
		if err != nil {
			return nil, report, err
		}
		for _, expansion := range contexts {
			spec, inferred, err := r.vmSpec(ctx, author, variablesForExpansion(variables, expansion), engine)
			if err != nil {
				return nil, report, resourceConversionError("vms."+author.Name, err)
			}
			id, err := expandedResourceID("vm", author.Name, expansion.key)
			if err != nil {
				return nil, report, err
			}
			dependencies, err := resolveDeclaredDependencies(author.DependsOn)
			if err != nil {
				return nil, report, err
			}
			dependencies = appendUniqueDependencies(dependencies, inferred...)
			if err := appendResource(pendingResource{id: id, dependencies: dependencies, required: required, spec: domain.ResourceSpec{VM: &spec}}); err != nil {
				return nil, report, err
			}
		}
	}

	for _, author := range lab.Spec.Resources.Containers {
		contexts, err := expandCommon(ctx, engine, author.AuthorCommon, variables)
		if err != nil {
			return nil, report, err
		}
		for _, expansion := range contexts {
			spec, inferred, err := r.containerSpec(ctx, author, variablesForExpansion(variables, expansion), engine)
			if err != nil {
				return nil, report, resourceConversionError("containers."+author.Name, err)
			}
			id, err := expandedResourceID("container", author.Name, expansion.key)
			if err != nil {
				return nil, report, err
			}
			dependencies, err := resolveDeclaredDependencies(author.DependsOn)
			if err != nil {
				return nil, report, err
			}
			dependencies = appendUniqueDependencies(dependencies, inferred...)
			if err := appendResource(pendingResource{id: id, dependencies: dependencies, required: required, spec: domain.ResourceSpec{Container: &spec}}); err != nil {
				return nil, report, err
			}
		}
	}

	for _, author := range lab.Spec.Resources.AccessEndpoints {
		contexts, err := expandCommon(ctx, engine, author.AuthorCommon, variables)
		if err != nil {
			return nil, report, err
		}
		for _, expansion := range contexts {
			spec, target, err := accessEndpointSpec(author)
			if err != nil {
				return nil, report, resourceConversionError("accessEndpoints."+author.Name, err)
			}
			id, err := expandedResourceID("access", author.Name, expansion.key)
			if err != nil {
				return nil, report, err
			}
			dependencies, err := resolveDeclaredDependencies(author.DependsOn)
			if err != nil {
				return nil, report, err
			}
			dependencies = appendUniqueDependencies(dependencies, target)
			if err := appendResource(pendingResource{id: id, dependencies: dependencies, required: required, spec: domain.ResourceSpec{AccessEndpoint: &spec}}); err != nil {
				return nil, report, err
			}
		}
	}

	for _, author := range lab.Spec.Outputs {
		name, err := domain.NewName(author.Name)
		if err != nil {
			return nil, report, resourceConversionError("outputs."+author.Name, err)
		}
		id, err := expandedResourceID("output", author.Name, "")
		if err != nil {
			return nil, report, err
		}
		dependencies := outputDependencies(author.Value)
		spec := domain.OutputSpec{Name: name, Type: author.Type, Expression: author.Value, Sensitive: author.Sensitive}
		if err := appendResource(pendingResource{id: id, dependencies: dependencies, required: required, spec: domain.ResourceSpec{Output: &spec}}); err != nil {
			return nil, report, err
		}
	}

	namespaces := make([]string, 0, len(lab.Spec.ProviderOverrides))
	for namespace := range lab.Spec.ProviderOverrides {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	for _, namespace := range namespaces {
		report.Warnings = append(report.Warnings, domain.ReportIssue{
			Code:      "provider_override_reduces_portability",
			Message:   fmt.Sprintf("provider override %q is target-specific", namespace),
			FieldPath: "spec.providerOverrides." + namespace,
		})
	}
	return pending, report, nil
}

func validateProviderOverrides(overrides map[string]AuthorProviderOverride) error {
	namespaces := make([]string, 0, len(overrides))
	for namespace := range overrides {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	for _, namespace := range namespaces {
		expectedPrefix := "provider.labcentral.io/" + namespace + "/"
		if !strings.HasPrefix(overrides[namespace].APIVersion, expectedPrefix) {
			return renderError(
				StageSchema,
				"provider_override_namespace_mismatch",
				"spec.providerOverrides."+namespace+".apiVersion",
				fmt.Sprintf("provider override namespace %q does not match apiVersion %q", namespace, overrides[namespace].APIVersion),
				nil,
			)
		}
	}
	return nil
}

func validateAuthoredReferences(resources AuthorResources, outputs []AuthorOutput) error {
	declared := make(map[domain.LogicalResourceID]struct{})
	for _, network := range resources.Networks {
		id, err := expandedResourceID("network", network.Name, "")
		if err != nil {
			return err
		}
		declared[id] = struct{}{}
	}
	for _, volume := range resources.Volumes {
		id, err := expandedResourceID("volume", volume.Name, "")
		if err != nil {
			return err
		}
		declared[id] = struct{}{}
	}
	for _, vm := range resources.VMs {
		id, err := expandedResourceID("vm", vm.Name, "")
		if err != nil {
			return err
		}
		declared[id] = struct{}{}
	}
	for _, container := range resources.Containers {
		id, err := expandedResourceID("container", container.Name, "")
		if err != nil {
			return err
		}
		declared[id] = struct{}{}
	}
	for _, access := range resources.AccessEndpoints {
		id, err := expandedResourceID("access", access.Name, "")
		if err != nil {
			return err
		}
		declared[id] = struct{}{}
	}
	check := func(owner, reference string) error {
		resolved, err := resolveReference(reference)
		if err != nil {
			return err
		}
		if _, exists := declared[resolved]; !exists {
			return renderError(StageReferenceResolution, "unresolved_reference", owner, fmt.Sprintf("referenced resource %q is not declared", resolved), nil)
		}
		return nil
	}
	for _, network := range resources.Networks {
		for _, dependency := range network.DependsOn {
			if err := check("networks."+network.Name, dependency); err != nil {
				return err
			}
		}
	}
	for _, volume := range resources.Volumes {
		for _, dependency := range volume.DependsOn {
			if err := check("volumes."+volume.Name, dependency); err != nil {
				return err
			}
		}
	}
	for _, vm := range resources.VMs {
		for _, dependency := range vm.DependsOn {
			if err := check("vms."+vm.Name, dependency); err != nil {
				return err
			}
		}
		for _, networkInterface := range vm.Interfaces {
			if err := check("vms."+vm.Name+".interfaces", networkInterface.NetworkRef); err != nil {
				return err
			}
		}
		for _, mount := range vm.Mounts {
			if err := check("vms."+vm.Name+".mounts", mount.VolumeRef); err != nil {
				return err
			}
		}
	}
	for _, container := range resources.Containers {
		for _, dependency := range container.DependsOn {
			if err := check("containers."+container.Name, dependency); err != nil {
				return err
			}
		}
		for _, networkInterface := range container.Interfaces {
			if err := check("containers."+container.Name+".interfaces", networkInterface.NetworkRef); err != nil {
				return err
			}
		}
		for _, mount := range container.Mounts {
			if err := check("containers."+container.Name+".mounts", mount.VolumeRef); err != nil {
				return err
			}
		}
	}
	for _, access := range resources.AccessEndpoints {
		for _, dependency := range access.DependsOn {
			if err := check("accessEndpoints."+access.Name, dependency); err != nil {
				return err
			}
		}
		if err := check("accessEndpoints."+access.Name, access.TargetRef); err != nil {
			return err
		}
	}
	for _, output := range outputs {
		for _, dependency := range outputDependencies(output.Value) {
			if _, exists := declared[dependency]; !exists {
				return renderError(StageReferenceResolution, "unresolved_reference", "outputs."+output.Name, fmt.Sprintf("referenced resource %q is not declared", dependency), nil)
			}
		}
	}
	return nil
}

func expandCommon(
	ctx context.Context,
	engine *ExpressionEngine,
	common AuthorCommon,
	variables ExpressionVariables,
) ([]expansionContext, error) {
	if common.ForEach == nil {
		if common.When != "" {
			included, err := engine.EvalBool(ctx, common.When, variables)
			if err != nil {
				return nil, err
			}
			if !included {
				return nil, nil
			}
		}
		return []expansionContext{{variables: cloneVariables(variables.Iteration)}}, nil
	}
	iterations, err := engine.EvalForEach(ctx, common.ForEach.In, variables)
	if err != nil {
		return nil, err
	}
	result := make([]expansionContext, 0, len(iterations))
	for _, iteration := range iterations {
		bindings := cloneVariables(variables.Iteration)
		bindings[common.ForEach.Key] = iteration.Key
		if common.ForEach.Value != "" {
			bindings[common.ForEach.Value] = iteration.Value
		}
		expandedVariables := variables
		expandedVariables.Iteration = bindings
		if common.When != "" {
			included, err := engine.EvalBool(ctx, common.When, expandedVariables)
			if err != nil {
				return nil, err
			}
			if !included {
				continue
			}
		}
		result = append(result, expansionContext{key: iteration.Key, variables: bindings})
	}
	return result, nil
}

func cloneVariables(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for name, value := range source {
		result[name] = value
	}
	return result
}

func variablesForExpansion(base ExpressionVariables, expansion expansionContext) ExpressionVariables {
	base.Iteration = expansion.variables
	return base
}

func capabilityExpression(capabilities []string) (domain.CapabilityExpression, error) {
	result := domain.CapabilityExpression{AllOf: make([]domain.CapabilityID, len(capabilities))}
	for index, raw := range capabilities {
		capability, err := domain.NewCapabilityID(raw)
		if err != nil {
			return domain.CapabilityExpression{}, renderError(StageSchema, "invalid_capability", "spec.requirements.capabilities", "capability ID is invalid", err)
		}
		result.AllOf[index] = capability
	}
	sort.Slice(result.AllOf, func(left, right int) bool { return result.AllOf[left] < result.AllOf[right] })
	if err := result.Validate(); err != nil {
		return domain.CapabilityExpression{}, renderError(StageSchema, "invalid_capability", "spec.requirements.capabilities", "capability expression is invalid", err)
	}
	return result, nil
}

func expandedResourceID(kind, name, iterationKey string) (domain.LogicalResourceID, error) {
	value := kind + "." + name
	if iterationKey != "" {
		value += "[" + iterationKey + "]"
	}
	id, err := domain.NewLogicalResourceID(value)
	if err != nil {
		return "", renderError(StageExpansion, "invalid_expanded_resource_id", value, "expanded resource ID is invalid", err)
	}
	return id, nil
}

func resolveDeclaredDependencies(references []string) ([]domain.LogicalResourceID, error) {
	result := make([]domain.LogicalResourceID, 0, len(references))
	for _, reference := range references {
		resolved, err := resolveReference(reference)
		if err != nil {
			return nil, err
		}
		result = appendUniqueDependencies(result, resolved)
	}
	return result, nil
}

var referenceKinds = map[string]string{
	"networks":        "network",
	"volumes":         "volume",
	"vms":             "vm",
	"containers":      "container",
	"accessEndpoints": "access",
}

func resolveReference(reference string) (domain.LogicalResourceID, error) {
	parts := strings.Split(reference, ".")
	if len(parts) != 2 {
		return "", renderError(StageReferenceResolution, "invalid_reference", reference, "reference must contain a resource collection and name", nil)
	}
	kind, exists := referenceKinds[parts[0]]
	if !exists {
		return "", renderError(StageReferenceResolution, "invalid_reference_kind", reference, "reference uses an unknown resource collection", nil)
	}
	id, err := domain.NewLogicalResourceID(kind + "." + parts[1])
	if err != nil {
		return "", renderError(StageReferenceResolution, "invalid_reference", reference, "reference does not form a valid logical resource ID", err)
	}
	return id, nil
}

func appendUniqueDependencies(existing []domain.LogicalResourceID, additions ...domain.LogicalResourceID) []domain.LogicalResourceID {
	seen := make(map[domain.LogicalResourceID]struct{}, len(existing)+len(additions))
	for _, dependency := range existing {
		seen[dependency] = struct{}{}
	}
	for _, dependency := range additions {
		if _, exists := seen[dependency]; !exists {
			existing = append(existing, dependency)
			seen[dependency] = struct{}{}
		}
	}
	sortResourceIDs(existing)
	return existing
}

func finalizeResources(pending []pendingResource) ([]domain.LogicalResource, error) {
	declared := make(map[domain.LogicalResourceID]struct{}, len(pending))
	for _, candidate := range pending {
		if _, duplicate := declared[candidate.id]; duplicate {
			return nil, renderError(StageExpansion, "duplicate_resource_id", string(candidate.id), "expanded resource ID is repeated", nil)
		}
		declared[candidate.id] = struct{}{}
	}
	resources := make([]domain.LogicalResource, 0, len(pending))
	for _, candidate := range pending {
		for _, dependency := range candidate.dependencies {
			if _, exists := declared[dependency]; !exists {
				return nil, renderError(
					StageReferenceResolution,
					"unresolved_reference_after_expansion",
					string(candidate.id),
					fmt.Sprintf("referenced resource %q was not produced", dependency),
					nil,
				)
			}
		}
		resource, err := domain.NewLogicalResource(candidate.id, candidate.dependencies, candidate.required, candidate.spec)
		if err != nil {
			return nil, resourceConversionError(string(candidate.id), err)
		}
		resources = append(resources, resource)
	}
	return resources, nil
}

func resourceConversionError(path string, err error) error {
	if renderErr, ok := err.(*Error); ok {
		return renderErr
	}
	return renderError(StageSchema, "invalid_canonical_resource", path, "resource could not be converted to the canonical domain", err)
}

func networkSpec(author AuthorNetwork) (domain.NetworkSpec, error) {
	name, err := domain.NewName(author.Name)
	if err != nil {
		return domain.NetworkSpec{}, err
	}
	kind := author.Kind
	if kind == "" {
		kind = domain.NetworkIsolated
	}
	ipam := author.IPAM
	if ipam == "" {
		ipam = domain.IPAMProviderManaged
	}
	egress := author.Egress.Mode
	if egress == "" {
		egress = domain.EgressDeny
	}
	spec := domain.NetworkSpec{
		Name: name,
		Kind: kind,
		IPAM: ipam,
		MTU:  author.MTU,
		Egress: domain.EgressPolicy{
			Mode:         egress,
			Destinations: append([]string(nil), author.Egress.Destinations...),
		},
	}
	if author.IPv4CIDR != "" {
		spec.IPv4CIDR, err = netip.ParsePrefix(author.IPv4CIDR)
		if err != nil {
			return domain.NetworkSpec{}, err
		}
		spec.IPv4CIDR = spec.IPv4CIDR.Masked()
	}
	if author.IPv6CIDR != "" {
		spec.IPv6CIDR, err = netip.ParsePrefix(author.IPv6CIDR)
		if err != nil {
			return domain.NetworkSpec{}, err
		}
		spec.IPv6CIDR = spec.IPv6CIDR.Masked()
	}
	return spec, spec.Validate()
}

var byteQuantityPattern = regexp.MustCompile(`^([1-9][0-9]*)(Mi|Gi|Ti)$`)

func parseByteQuantity(value string) (domain.ByteQuantity, error) {
	match := byteQuantityPattern.FindStringSubmatch(value)
	if match == nil {
		return 0, fmt.Errorf("byte quantity must use Mi, Gi, or Ti")
	}
	amount, err := strconv.ParseUint(match[1], 10, 64)
	if err != nil {
		return 0, err
	}
	multiplier := uint64(1 << 20)
	switch match[2] {
	case "Gi":
		multiplier = 1 << 30
	case "Ti":
		multiplier = 1 << 40
	}
	if amount > math.MaxUint64/multiplier {
		return 0, fmt.Errorf("byte quantity overflows uint64")
	}
	return domain.NewByteQuantity(amount * multiplier)
}

func volumeSpec(author AuthorVolume) (domain.VolumeSpec, error) {
	name, err := domain.NewName(author.Name)
	if err != nil {
		return domain.VolumeSpec{}, err
	}
	size, err := parseByteQuantity(author.Size)
	if err != nil {
		return domain.VolumeSpec{}, err
	}
	persistence := author.Persistence
	if persistence == "" {
		persistence = domain.VolumeEphemeral
	}
	spec := domain.VolumeSpec{Name: name, SizeBytes: size, Persistence: persistence, SnapshotReady: author.Snapshot}
	return spec, spec.Validate()
}

func computeRequirements(author AuthorCompute) (domain.ComputeRequirements, error) {
	millisFloat := author.CPU * 1000
	if math.IsNaN(millisFloat) || math.IsInf(millisFloat, 0) || millisFloat != math.Trunc(millisFloat) || millisFloat > math.MaxUint32 {
		return domain.ComputeRequirements{}, fmt.Errorf("CPU must resolve to a whole number of millicores")
	}
	memory, err := parseByteQuantity(author.Memory)
	if err != nil {
		return domain.ComputeRequirements{}, err
	}
	result := domain.ComputeRequirements{CPUMillis: uint32(millisFloat), MemoryBytes: memory, Architecture: author.Architecture}
	return result, result.Validate()
}

func (r *Renderer) vmSpec(
	ctx context.Context,
	author AuthorVM,
	variables ExpressionVariables,
	engine *ExpressionEngine,
) (domain.VMSpec, []domain.LogicalResourceID, error) {
	name, err := domain.NewName(author.Name)
	if err != nil {
		return domain.VMSpec{}, nil, err
	}
	image, err := domain.NewImageRef(author.ImageRef)
	if err != nil {
		return domain.VMSpec{}, nil, err
	}
	compute, err := computeRequirements(author.Compute)
	if err != nil {
		return domain.VMSpec{}, nil, err
	}
	interfaces, interfaceDependencies, err := r.networkInterfaces(ctx, author.Interfaces, variables, engine)
	if err != nil {
		return domain.VMSpec{}, nil, err
	}
	mounts, mountDependencies, err := mounts(author.Mounts)
	if err != nil {
		return domain.VMSpec{}, nil, err
	}
	var provisioning *domain.ProvisioningSpec
	if author.Provisioning != nil {
		artifact, err := domain.NewArtifactRef(author.Provisioning.ArtifactRef)
		if err != nil {
			return domain.VMSpec{}, nil, err
		}
		provisioning = &domain.ProvisioningSpec{Method: author.Provisioning.Method, ArtifactRef: artifact}
	}
	spec := domain.VMSpec{
		Name:         name,
		Image:        image,
		OSFamily:     author.OSFamily,
		Compute:      compute,
		Interfaces:   interfaces,
		Mounts:       mounts,
		Provisioning: provisioning,
		Lifecycle: domain.WorkloadLifecycle{
			Snapshot: author.Lifecycle.Snapshot,
			Reset:    domain.ResetMode(author.Lifecycle.Reset),
		},
	}
	dependencies := appendUniqueDependencies(interfaceDependencies, mountDependencies...)
	return spec, dependencies, spec.Validate()
}

func (r *Renderer) containerSpec(
	ctx context.Context,
	author AuthorContainer,
	variables ExpressionVariables,
	engine *ExpressionEngine,
) (domain.ContainerSpec, []domain.LogicalResourceID, error) {
	name, err := domain.NewName(author.Name)
	if err != nil {
		return domain.ContainerSpec{}, nil, err
	}
	image, err := domain.NewImageRef(author.Image)
	if err != nil {
		return domain.ContainerSpec{}, nil, err
	}
	compute, err := computeRequirements(author.Compute)
	if err != nil {
		return domain.ContainerSpec{}, nil, err
	}
	interfaces, interfaceDependencies, err := r.networkInterfaces(ctx, author.Interfaces, variables, engine)
	if err != nil {
		return domain.ContainerSpec{}, nil, err
	}
	resolvedMounts, mountDependencies, err := mounts(author.Mounts)
	if err != nil {
		return domain.ContainerSpec{}, nil, err
	}
	environmentNames := make([]string, 0, len(author.Environment))
	for environmentName := range author.Environment {
		environmentNames = append(environmentNames, environmentName)
	}
	sort.Strings(environmentNames)
	environment := make([]domain.EnvironmentVariable, 0, len(environmentNames))
	for _, environmentName := range environmentNames {
		value, err := decodeEnvironmentValue(author.Environment[environmentName])
		if err != nil {
			return domain.ContainerSpec{}, nil, err
		}
		environment = append(environment, domain.EnvironmentVariable{Name: environmentName, Value: value})
	}
	ports := make([]domain.PublishedPort, len(author.PublishedPorts))
	for index, port := range author.PublishedPorts {
		name, err := domain.NewName(port.Name)
		if err != nil {
			return domain.ContainerSpec{}, nil, err
		}
		protocol := port.Protocol
		if protocol == "" {
			protocol = domain.ProtocolTCP
		}
		ports[index] = domain.PublishedPort{
			Name:         name,
			Port:         port.Port,
			Protocol:     protocol,
			Exposure:     port.Exposure,
			ExternalPort: port.ExternalPort,
		}
	}
	spec := domain.ContainerSpec{
		Name:                  name,
		Image:                 image,
		Compute:               compute,
		Isolation:             author.Isolation,
		RealizationPreference: append([]domain.ContainerRealization(nil), author.RealizationPreference...),
		Interfaces:            interfaces,
		Mounts:                resolvedMounts,
		Environment:           environment,
		Command:               append([]string(nil), author.Command...),
		Args:                  append([]string(nil), author.Args...),
		PublishedPorts:        ports,
	}
	dependencies := appendUniqueDependencies(interfaceDependencies, mountDependencies...)
	return spec, dependencies, spec.Validate()
}

func (r *Renderer) networkInterfaces(
	ctx context.Context,
	authored []AuthorInterface,
	variables ExpressionVariables,
	engine *ExpressionEngine,
) ([]domain.NetworkInterface, []domain.LogicalResourceID, error) {
	interfaces := make([]domain.NetworkInterface, 0, len(authored))
	dependencies := make([]domain.LogicalResourceID, 0, len(authored))
	for _, author := range authored {
		if author.When != "" {
			included, err := engine.EvalBool(ctx, author.When, variables)
			if err != nil {
				return nil, nil, err
			}
			if !included {
				continue
			}
		}
		networkID, err := resolveReference(author.NetworkRef)
		if err != nil {
			return nil, nil, err
		}
		resolved := domain.NetworkInterface{NetworkID: networkID, MAC: author.MAC}
		if author.IPv4 != "" {
			resolved.IPv4, err = netip.ParseAddr(author.IPv4)
			if err != nil {
				return nil, nil, err
			}
		}
		if author.IPv6 != "" {
			resolved.IPv6, err = netip.ParseAddr(author.IPv6)
			if err != nil {
				return nil, nil, err
			}
		}
		if err := resolved.Validate(); err != nil {
			return nil, nil, err
		}
		interfaces = append(interfaces, resolved)
		dependencies = appendUniqueDependencies(dependencies, networkID)
	}
	return interfaces, dependencies, nil
}

func mounts(authored []AuthorMount) ([]domain.Mount, []domain.LogicalResourceID, error) {
	result := make([]domain.Mount, len(authored))
	dependencies := make([]domain.LogicalResourceID, 0, len(authored))
	for index, author := range authored {
		volumeID, err := resolveReference(author.VolumeRef)
		if err != nil {
			return nil, nil, err
		}
		mount := domain.Mount{VolumeID: volumeID, Path: author.Path, ReadOnly: author.ReadOnly}
		if err := mount.Validate(); err != nil {
			return nil, nil, err
		}
		result[index] = mount
		dependencies = appendUniqueDependencies(dependencies, volumeID)
	}
	return result, dependencies, nil
}

func accessEndpointSpec(author AuthorAccessEndpoint) (domain.AccessEndpointSpec, domain.LogicalResourceID, error) {
	name, err := domain.NewName(author.Name)
	if err != nil {
		return domain.AccessEndpointSpec{}, "", err
	}
	target, err := resolveReference(author.TargetRef)
	if err != nil {
		return domain.AccessEndpointSpec{}, "", err
	}
	spec := domain.AccessEndpointSpec{Name: name, TargetID: target, Methods: append([]domain.AccessMethod(nil), author.Methods...)}
	return spec, target, spec.Validate()
}

var outputReferencePattern = regexp.MustCompile(`^resources\.(networks|volumes|vms|containers|accessEndpoints)\.([a-z][a-z0-9-]{0,62})(\.|\[|$)`)

func outputDependencies(expression string) []domain.LogicalResourceID {
	match := outputReferencePattern.FindStringSubmatch(expression)
	if match == nil {
		return nil
	}
	dependency, err := resolveReference(match[1] + "." + match[2])
	if err != nil {
		return nil
	}
	return []domain.LogicalResourceID{dependency}
}

var _ domain.LabRenderer = (*Renderer)(nil)
