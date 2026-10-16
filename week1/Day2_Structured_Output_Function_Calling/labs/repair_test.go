package main

import (
	"strings"
	"testing"
)

func TestValidationRepairPipeline_FaultInjection(t *testing.T) {
	tests := []struct {
		name       string
		payload    RawToolPayload
		maxRetries int
		repairFn   RepairFunc
		wantErr    bool
		errSubstr  string
		wantBase   string
		wantTarget string
	}{
		{
			name: "Scenario 1: Missing required field (target)",
			payload: RawToolPayload{
				"base": "USD",
				// "target" пропущено
			},
			maxRetries: 2,
			repairFn:   DefaultRepairer,
			wantErr:    false,
			wantBase:   "USD",
			wantTarget: "USD", // відновлено дефолтом
		},
		{
			name: "Scenario 2: Invalid type (numeric base instead of string)",
			payload: RawToolPayload{
				"base":   840, // ISO числовий код замість рядка "USD"
				"target": "UAH",
			},
			maxRetries: 1,
			repairFn:   DefaultRepairer,
			wantErr:    false,
			wantBase:   "840",
			wantTarget: "UAH",
		},
		{
			name: "Scenario 3: Unknown enum value (source_kind = 'external_service')",
			payload: RawToolPayload{
				"base":        "EUR",
				"target":      "UAH",
				"source_kind": "external_service", // неприпустимий enum
			},
			maxRetries: 1,
			repairFn:   DefaultRepairer,
			wantErr:    false,
			wantBase:   "EUR",
			wantTarget: "UAH",
		},
		{
			name: "Scenario 4: Exhausted retries without repair (strict fail path)",
			payload: RawToolPayload{
				"base": nil,
			},
			maxRetries: 1,
			repairFn: func(p RawToolPayload, errs []ValidationError) (RawToolPayload, error) {
				// Ремонт не усуває помилку навмисно
				return p, nil
			},
			wantErr:   true,
			errSubstr: "validation failed after 1 retries",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ValidationRepairPipeline(tt.payload, tt.maxRetries, tt.repairFn)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.errSubstr) {
					t.Fatalf("expected error containing %q, got %q", tt.errSubstr, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Base != tt.wantBase {
				t.Errorf("expected Base=%q, got %q", tt.wantBase, result.Base)
			}
			if result.Target != tt.wantTarget {
				t.Errorf("expected Target=%q, got %q", tt.wantTarget, result.Target)
			}
		})
	}
}
