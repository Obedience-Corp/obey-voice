package decision

import (
	"context"
	"testing"
)

func TestRouteChoiceUsesItsOwnConfidenceGateAndPreservesEvidence(t *testing.T) {
	for _, mode := range []string{"accepted", "low-confidence", "missing", "unavailable", "bad-distribution", "none"} {
		t.Run(mode, func(t *testing.T) {
			route := certain(Simple).Answers["tier"]
			switch mode {
			case "low-confidence":
				v := .89
				route.Confidence = &v
			case "missing":
				route.Confidence = nil
			case "unavailable":
				route = certain(Complex).Answers["tier"]
			case "bad-distribution":
				route.Probabilities["medium"] = 1
			case "none":
				route.Choice = "none"
				route.Probabilities["none"] = 1
				route.Probabilities["simple"] = 0
			}
			provider := evaluatorFunc(func(_ context.Context, req Request) (Response, error) {
				if len(req.Questions) != 1 || req.Questions["route"].Type != "choice" {
					t.Fatalf("wrong policy questions=%+v", req.Questions)
				}
				return Response{Model: "fixture", Answers: map[string]Answer{"route": route}}, nil
			})
			got, err := (WorkPolicy{Evaluator: provider, Mode: PolicyRouteChoice}).Decide(t.Context(), sampleInput())
			if got.Accepted != (mode == "accepted") {
				t.Fatalf("proposal=%+v err=%v", got, err)
			}
			if mode == "accepted" && (got.Evidence.Model != "fixture" || len(got.Evidence.Answers) != 1 || got.Evidence.Answers["work"].Noul != nil) {
				t.Fatal("acceptance manufactured model evidence")
			}
			if (mode == "missing" || mode == "bad-distribution") && err == nil {
				t.Fatal("malformed response accepted as valid")
			}
		})
	}
}
