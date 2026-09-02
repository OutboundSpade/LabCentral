package render

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestExpressionEngineEvaluatesBoundedConditions(t *testing.T) {
	t.Parallel()
	engine, err := NewExpressionEngine(DefaultExpressionLimits())
	if err != nil {
		t.Fatal(err)
	}
	actual, err := engine.EvalBool(context.Background(), "params.controlled_egress", ExpressionVariables{
		Params: map[string]any{"controlled_egress": false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if actual {
		t.Fatal("false condition evaluated true")
	}
}

func TestRenderContract003SecretValueUnavailable(t *testing.T) {
	t.Parallel()
	engine, err := NewExpressionEngine(DefaultExpressionLimits())
	if err != nil {
		t.Fatal(err)
	}
	variables := ExpressionVariables{Params: map[string]any{}, SecretParameters: map[string]struct{}{"database_password": {}}}
	for _, expression := range []string{"params.database_password", `params["database_password"]`} {
		_, err := engine.EvalBool(context.Background(), expression, variables)
		var renderError *Error
		if !errors.As(err, &renderError) || renderError.Code != "secret_value_unavailable" {
			t.Errorf("got %v, want secret_value_unavailable", err)
		}
	}
}

func TestRenderContract004ForbiddenHostFunction(t *testing.T) {
	t.Parallel()
	engine, err := NewExpressionEngine(DefaultExpressionLimits())
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.EvalBool(context.Background(), `http.get("http://169.254.169.254/")`, ExpressionVariables{Params: map[string]any{}})
	var renderError *Error
	if !errors.As(err, &renderError) || renderError.Code != "undeclared_function" {
		t.Fatalf("got %v, want undeclared_function", err)
	}
}

func TestRenderContract005StableForEachIdentity(t *testing.T) {
	t.Parallel()
	engine, err := NewExpressionEngine(DefaultExpressionLimits())
	if err != nil {
		t.Fatal(err)
	}
	iterations, err := engine.EvalForEach(context.Background(), "params.loadgen_ids", ExpressionVariables{
		Params: map[string]any{"loadgen_ids": []string{"two", "one"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, len(iterations))
	for index := range iterations {
		keys[index] = iterations[index].Key
	}
	if !reflect.DeepEqual(keys, []string{"one", "two"}) {
		t.Fatalf("iteration keys = %v", keys)
	}
}

func TestExpressionEngineRejectsOversizedExpression(t *testing.T) {
	t.Parallel()
	limits := DefaultExpressionLimits()
	limits.MaxCodePoints = 4
	engine, err := NewExpressionEngine(limits)
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.EvalBool(context.Background(), "true && true", ExpressionVariables{Params: map[string]any{}})
	var renderError *Error
	if !errors.As(err, &renderError) || renderError.Stage != StageCELTypecheck {
		t.Fatalf("oversized expression returned unexpected error: %v", err)
	}
}
