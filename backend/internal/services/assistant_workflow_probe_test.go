package services

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/compose"
	"github.com/shopspring/decimal"
)

type einoProbeIn struct {
	Amount decimal.Decimal
}

type einoProbeMid struct {
	Amount decimal.Decimal
}

type einoProbeOut struct {
	Amount decimal.Decimal
}

// TestEinoWorkflowProbe compiles and runs one acyclic Workflow: two lambdas and
// one decimal field mapping. No business nodes, models, or databases.
func TestEinoWorkflowProbe(t *testing.T) {
	wf := compose.NewWorkflow[einoProbeIn, einoProbeOut]()

	wf.AddLambdaNode("echo", compose.InvokableLambda(func(ctx context.Context, in einoProbeIn) (einoProbeMid, error) {
		return einoProbeMid{Amount: in.Amount}, nil
	})).AddInput(compose.START)

	wf.AddLambdaNode("scale", compose.InvokableLambda(func(ctx context.Context, in einoProbeMid) (einoProbeOut, error) {
		return einoProbeOut{Amount: in.Amount.Mul(decimal.NewFromInt(2))}, nil
	})).AddInput("echo", compose.MapFields("Amount", "Amount"))

	wf.End().AddInput("scale")

	run, err := wf.Compile(context.Background())
	if err != nil {
		t.Fatalf("compile probe workflow: %v", err)
	}

	got, err := run.Invoke(context.Background(), einoProbeIn{
		Amount: decimal.RequireFromString("1.25"),
	})
	if err != nil {
		t.Fatalf("invoke probe workflow: %v", err)
	}

	want := decimal.RequireFromString("2.50")
	if !got.Amount.Equal(want) {
		t.Fatalf("mapped Amount = %s, want %s", got.Amount.String(), want.String())
	}
}
