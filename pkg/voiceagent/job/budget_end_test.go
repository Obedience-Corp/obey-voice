package job

import (
	"context"
	"testing"
	"time"
)

func TestRunSeesItsBudgetEnd(t *testing.T) {
	for name, tc := range map[string]struct {
		table, pinned time.Duration
		want          time.Duration
	}{
		"table budget":  {table: time.Hour, want: time.Hour},
		"pinned budget": {table: time.Hour, pinned: time.Minute, want: time.Minute},
		"no budget":     {},
	} {
		t.Run(name, func(t *testing.T) {
			table := NewTable(t.Context(), WithBudget(tc.table))
			defer table.Close()
			type seen struct {
				end time.Time
				ok  bool
			}
			got := make(chan seen, 1)
			before := time.Now()
			_, err := table.Start(t.Context(), Spec{RuntimeBudget: tc.pinned, Run: func(ctx context.Context) (string, error) {
				end, ok := BudgetEnd(ctx)
				got <- seen{end, ok}
				return "", nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			s := <-got
			if tc.want == 0 {
				if s.ok {
					t.Fatalf("budget end %v reported without a budget", s.end)
				}
				return
			}
			if !s.ok || s.end.Before(before.Add(tc.want)) || s.end.After(time.Now().Add(tc.want)) {
				t.Fatalf("budget end=%v ok=%v, want about %v from start", s.end, s.ok, tc.want)
			}
		})
	}
	if _, ok := BudgetEnd(context.Background()); ok {
		t.Fatal("budget end reported outside a job")
	}
}
