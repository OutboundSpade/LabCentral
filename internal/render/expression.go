package render

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/cel-go/cel"
	exprpb "google.golang.org/genproto/googleapis/api/expr/v1alpha1"
)

type ExpressionLimits struct {
	MaxCodePoints           int
	MaxASTDepth             int
	MaxComprehensionNesting int
	MaxCost                 uint64
	MaxCollectionItems      int
}

func DefaultExpressionLimits() ExpressionLimits {
	return ExpressionLimits{
		MaxCodePoints:           1024,
		MaxASTDepth:             64,
		MaxComprehensionNesting: 4,
		MaxCost:                 10_000,
		MaxCollectionItems:      10_000,
	}
}

type ExpressionVariables struct {
	Params           map[string]any
	Iteration        map[string]any
	SecretParameters map[string]struct{}
}

type Iteration struct {
	Key   string
	Value any
}

type ExpressionEngine struct {
	limits ExpressionLimits
}

func NewExpressionEngine(limits ExpressionLimits) (*ExpressionEngine, error) {
	if limits.MaxCodePoints <= 0 || limits.MaxASTDepth <= 0 || limits.MaxComprehensionNesting < 0 || limits.MaxCost == 0 || limits.MaxCollectionItems <= 0 {
		return nil, renderError(StageCELTypecheck, "invalid_cel_limits", "", "CEL limits must be positive", nil)
	}
	return &ExpressionEngine{limits: limits}, nil
}

func (e *ExpressionEngine) EvalBool(ctx context.Context, expression string, variables ExpressionVariables) (bool, error) {
	value, outputType, err := e.evaluate(ctx, expression, variables)
	if err != nil {
		return false, err
	}
	if !outputType.IsExactType(cel.BoolType) {
		return false, renderError(StageCELTypecheck, "condition_not_boolean", "", "condition expression must have Boolean type", nil)
	}
	result, ok := value.(bool)
	if !ok {
		return false, renderError(StageCELEvaluation, "condition_not_boolean", "", "condition expression returned a non-Boolean value", nil)
	}
	return result, nil
}

func (e *ExpressionEngine) EvalForEach(ctx context.Context, expression string, variables ExpressionVariables) ([]Iteration, error) {
	value, _, err := e.evaluate(ctx, expression, variables)
	if err != nil {
		return nil, err
	}
	var iterations []Iteration
	switch collection := value.(type) {
	case []string:
		iterations = make([]Iteration, 0, len(collection))
		for _, item := range collection {
			iterations = append(iterations, Iteration{Key: item, Value: item})
		}
	case []any:
		iterations = make([]Iteration, 0, len(collection))
		for _, item := range collection {
			key, ok := item.(string)
			if !ok {
				return nil, renderError(StageExpansion, "unstable_iteration_key", "", "list iteration requires unique string values as stable keys", nil)
			}
			iterations = append(iterations, Iteration{Key: key, Value: item})
		}
	case map[string]any:
		iterations = make([]Iteration, 0, len(collection))
		for key, item := range collection {
			iterations = append(iterations, Iteration{Key: key, Value: item})
		}
	default:
		return nil, renderError(StageCELTypecheck, "foreach_not_collection", "", "forEach expression must return a list or string-keyed map", nil)
	}
	if len(iterations) > e.limits.MaxCollectionItems {
		return nil, renderError(StageExpansion, "expansion_limit_exceeded", "", "forEach collection exceeds the configured expansion limit", nil)
	}
	sort.Slice(iterations, func(left, right int) bool { return iterations[left].Key < iterations[right].Key })
	for index := 1; index < len(iterations); index++ {
		if iterations[index-1].Key == iterations[index].Key {
			return nil, renderError(StageExpansion, "duplicate_iteration_key", "", fmt.Sprintf("forEach stable key %q is repeated", iterations[index].Key), nil)
		}
	}
	return iterations, nil
}

func (e *ExpressionEngine) evaluate(
	ctx context.Context,
	expression string,
	variables ExpressionVariables,
) (any, *cel.Type, error) {
	baseOptions := []cel.EnvOption{
		cel.Variable("params", cel.MapType(cel.StringType, cel.DynType)),
		cel.ParserExpressionSizeLimit(e.limits.MaxCodePoints),
		cel.ParserRecursionLimit(e.limits.MaxASTDepth),
		cel.ASTValidators(cel.ValidateComprehensionNestingLimit(e.limits.MaxComprehensionNesting)),
	}
	activation := map[string]any{"params": variables.Params}
	for name, value := range variables.Iteration {
		if name == "params" {
			return nil, nil, renderError(StageCELTypecheck, "reserved_variable", "", "iteration variable cannot be named params", nil)
		}
		baseOptions = append(baseOptions, cel.Variable(name, cel.DynType))
		activation[name] = value
	}
	inspectionEnvironment, err := cel.NewEnv(baseOptions...)
	if err != nil {
		return nil, nil, renderError(StageCELTypecheck, "cel_environment_invalid", "", "CEL environment could not be created", err)
	}
	inspectionAST, issues := inspectionEnvironment.Compile(expression)
	if issues != nil && issues.Err() != nil {
		code := "invalid_expression"
		issueText := issues.String()
		if strings.Contains(issueText, "undeclared reference") || strings.Contains(issueText, "no matching overload") {
			code = "undeclared_function"
		}
		return nil, nil, renderError(StageCELTypecheck, code, "", "CEL expression did not type-check", issues.Err())
	}
	parsed, err := cel.AstToParsedExpr(inspectionAST)
	if err != nil {
		return nil, nil, renderError(StageCELTypecheck, "invalid_expression", "", "CEL expression could not be inspected", err)
	}
	if secretName, found := referencedSecret(parsed.Expr, variables.SecretParameters); found {
		return nil, nil, renderError(StageCELTypecheck, "secret_value_unavailable", "", fmt.Sprintf("secret parameter %q is not available to CEL", secretName), nil)
	}
	parameterType := cel.DynType
	referenced := referencedParameterNames(parsed.Expr)
	if len(referenced) > 0 {
		var inferred *cel.Type
		for name := range referenced {
			value, exists := variables.Params[name]
			if !exists {
				return nil, nil, renderError(StageCELTypecheck, "undeclared_parameter", "", fmt.Sprintf("parameter %q is not declared", name), nil)
			}
			current := inferCELType(value)
			if inferred == nil {
				inferred = current
			} else if !inferred.IsExactType(current) {
				inferred = cel.DynType
			}
		}
		parameterType = inferred
	}
	typedOptions := []cel.EnvOption{
		cel.Variable("params", cel.MapType(cel.StringType, parameterType)),
		cel.ParserExpressionSizeLimit(e.limits.MaxCodePoints),
		cel.ParserRecursionLimit(e.limits.MaxASTDepth),
		cel.ASTValidators(cel.ValidateComprehensionNestingLimit(e.limits.MaxComprehensionNesting)),
	}
	for name, value := range variables.Iteration {
		typedOptions = append(typedOptions, cel.Variable(name, inferCELType(value)))
	}
	environment, err := cel.NewEnv(typedOptions...)
	if err != nil {
		return nil, nil, renderError(StageCELTypecheck, "cel_environment_invalid", "", "typed CEL environment could not be created", err)
	}
	ast, issues := environment.Compile(expression)
	if issues != nil && issues.Err() != nil {
		return nil, nil, renderError(StageCELTypecheck, "invalid_expression", "", "CEL expression did not type-check against parameter types", issues.Err())
	}
	program, err := environment.Program(
		ast,
		cel.CostLimit(e.limits.MaxCost),
		cel.EvalOptions(cel.OptTrackCost),
		cel.InterruptCheckFrequency(100),
	)
	if err != nil {
		return nil, nil, renderError(StageCELTypecheck, "invalid_expression", "", "CEL program could not be created", err)
	}
	result, _, err := program.ContextEval(ctx, activation)
	if err != nil {
		code := "evaluation_failed"
		if strings.Contains(err.Error(), "cost limit") {
			code = "cost_limit_exceeded"
		}
		return nil, nil, renderError(StageCELEvaluation, code, "", "CEL expression evaluation failed", err)
	}
	return result.Value(), ast.OutputType(), nil
}

func inferCELType(value any) *cel.Type {
	switch typed := value.(type) {
	case bool:
		return cel.BoolType
	case string:
		return cel.StringType
	case int, int8, int16, int32, int64:
		return cel.IntType
	case uint, uint8, uint16, uint32, uint64:
		return cel.UintType
	case float32, float64:
		return cel.DoubleType
	case []string:
		return cel.ListType(cel.StringType)
	case []any:
		if len(typed) == 0 {
			return cel.ListType(cel.DynType)
		}
		elementType := inferCELType(typed[0])
		for _, element := range typed[1:] {
			if !elementType.IsExactType(inferCELType(element)) {
				elementType = cel.DynType
				break
			}
		}
		return cel.ListType(elementType)
	case map[string]any:
		return cel.MapType(cel.StringType, cel.DynType)
	default:
		return cel.DynType
	}
}

func referencedParameterNames(expression *exprpb.Expr) map[string]struct{} {
	names := make(map[string]struct{})
	collectReferencedParameters(expression, names)
	return names
}

func collectReferencedParameters(expression *exprpb.Expr, names map[string]struct{}) {
	if expression == nil {
		return
	}
	if selection := expression.GetSelectExpr(); selection != nil {
		if identifier := selection.Operand.GetIdentExpr(); identifier != nil && identifier.Name == "params" {
			names[selection.Field] = struct{}{}
		}
		collectReferencedParameters(selection.Operand, names)
	}
	if call := expression.GetCallExpr(); call != nil {
		if call.Function == "_[_]" && len(call.Args) == 2 {
			identifier := call.Args[0].GetIdentExpr()
			key := call.Args[1].GetConstExpr()
			if identifier != nil && identifier.Name == "params" && key != nil && key.GetStringValue() != "" {
				names[key.GetStringValue()] = struct{}{}
			}
		}
		collectReferencedParameters(call.Target, names)
		for _, argument := range call.Args {
			collectReferencedParameters(argument, names)
		}
	}
	if list := expression.GetListExpr(); list != nil {
		for _, element := range list.Elements {
			collectReferencedParameters(element, names)
		}
	}
	if object := expression.GetStructExpr(); object != nil {
		for _, entry := range object.Entries {
			collectReferencedParameters(entry.GetMapKey(), names)
			collectReferencedParameters(entry.Value, names)
		}
	}
	if comprehension := expression.GetComprehensionExpr(); comprehension != nil {
		for _, child := range []*exprpb.Expr{
			comprehension.IterRange,
			comprehension.AccuInit,
			comprehension.LoopCondition,
			comprehension.LoopStep,
			comprehension.Result,
		} {
			collectReferencedParameters(child, names)
		}
	}
}

func referencedSecret(expression *exprpb.Expr, secretNames map[string]struct{}) (string, bool) {
	if expression == nil || len(secretNames) == 0 {
		return "", false
	}
	if selection := expression.GetSelectExpr(); selection != nil {
		if identifier := selection.Operand.GetIdentExpr(); identifier != nil && identifier.Name == "params" {
			if _, secret := secretNames[selection.Field]; secret {
				return selection.Field, true
			}
		}
		if name, found := referencedSecret(selection.Operand, secretNames); found {
			return name, true
		}
	}
	if call := expression.GetCallExpr(); call != nil {
		if call.Function == "_[_]" && len(call.Args) == 2 {
			identifier := call.Args[0].GetIdentExpr()
			key := call.Args[1].GetConstExpr()
			if identifier != nil && identifier.Name == "params" && key != nil {
				if _, secret := secretNames[key.GetStringValue()]; secret {
					return key.GetStringValue(), true
				}
			}
		}
		if name, found := referencedSecret(call.Target, secretNames); found {
			return name, true
		}
		for _, argument := range call.Args {
			if name, found := referencedSecret(argument, secretNames); found {
				return name, true
			}
		}
	}
	if list := expression.GetListExpr(); list != nil {
		for _, element := range list.Elements {
			if name, found := referencedSecret(element, secretNames); found {
				return name, true
			}
		}
	}
	if object := expression.GetStructExpr(); object != nil {
		for _, entry := range object.Entries {
			if name, found := referencedSecret(entry.GetMapKey(), secretNames); found {
				return name, true
			}
			if name, found := referencedSecret(entry.Value, secretNames); found {
				return name, true
			}
		}
	}
	if comprehension := expression.GetComprehensionExpr(); comprehension != nil {
		children := []*exprpb.Expr{
			comprehension.IterRange,
			comprehension.AccuInit,
			comprehension.LoopCondition,
			comprehension.LoopStep,
			comprehension.Result,
		}
		for _, child := range children {
			if name, found := referencedSecret(child, secretNames); found {
				return name, true
			}
		}
	}
	return "", false
}
