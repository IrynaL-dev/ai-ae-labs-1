package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// AllowedSourceKinds містить допустимі значення enum для поля Kind.
var AllowedSourceKinds = map[string]bool{
	string(SourceKindAPI):   true,
	string(SourceKindCache): true,
	string(SourceKindMock):  true,
}

// ValidationError описує дефект у вхідних даних виклику інструмента.
type ValidationError struct {
	Field   string
	Message string
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("field '%s': %s", e.Field, e.Message)
}

// RawToolPayload представляє вхідний виклик інструмента перед ремонтом.
type RawToolPayload map[string]interface{}

// ValidatePayload перевіряє наявність полів, коректність типів та значення enum.
func ValidatePayload(raw RawToolPayload) []ValidationError {
	var errs []ValidationError

	// 1. Перевірка обов'язкових полів: base та target
	baseRaw, okBase := raw["base"]
	if !okBase || baseRaw == nil {
		errs = append(errs, ValidationError{Field: "base", Message: "field is missing"})
	} else if _, isStr := baseRaw.(string); !isStr {
		errs = append(errs, ValidationError{Field: "base", Message: "invalid type, expected string"})
	}

	targetRaw, okTarget := raw["target"]
	if !okTarget || targetRaw == nil {
		errs = append(errs, ValidationError{Field: "target", Message: "field is missing"})
	} else if _, isStr := targetRaw.(string); !isStr {
		errs = append(errs, ValidationError{Field: "target", Message: "invalid type, expected string"})
	}

	// 2. Перевірка enum (якщо передано опційне поле source_kind)
	if kindRaw, ok := raw["source_kind"]; ok && kindRaw != nil {
		kindStr, isStr := kindRaw.(string)
		if !isStr {
			errs = append(errs, ValidationError{Field: "source_kind", Message: "invalid type, expected string"})
		} else if !AllowedSourceKinds[strings.ToLower(kindStr)] {
			errs = append(errs, ValidationError{Field: "source_kind", Message: fmt.Sprintf("unknown enum value '%s'", kindStr)})
		}
	}

	return errs
}

// RepairFunc — функція відновлення, що усуває виявлені дефекти.
type RepairFunc func(payload RawToolPayload, errs []ValidationError) (RawToolPayload, error)

// DefaultRepairer виправляє типові помилки галюцинацій моделі:
// - конвертує нерядкові типи в рядки;
// - підставляє дефолтне поле, якщо воно відсутнє;
// - нормалізує або скидає невідомий enum до допустимого fallback.
func DefaultRepairer(payload RawToolPayload, errs []ValidationError) (RawToolPayload, error) {
	repaired := make(RawToolPayload)
	for k, v := range payload {
		repaired[k] = v
	}

	for _, err := range errs {
		switch err.Field {
		case "base":
			if val, exists := repaired["base"]; exists && val != nil {
				repaired["base"] = fmt.Sprintf("%v", val)
			} else {
				repaired["base"] = "UAH" // Безпечний дефолт
			}
		case "target":
			if val, exists := repaired["target"]; exists && val != nil {
				repaired["target"] = fmt.Sprintf("%v", val)
			} else {
				repaired["target"] = "USD" // Безпечний дефолт
			}
		case "source_kind":
			repaired["source_kind"] = string(SourceKindAPI) // Fallback до валідного enum
		}
	}

	return repaired, nil
}

// ValidationRepairPipeline виконує цикл: валідація -> ремонт -> повторна валідація.
func ValidationRepairPipeline(raw RawToolPayload, maxRetries int, repairFn RepairFunc) (RateInput, error) {
	current := raw
	var lastErrs []ValidationError

	for attempt := 0; attempt <= maxRetries; attempt++ {
		lastErrs = ValidatePayload(current)
		if len(lastErrs) == 0 {
			// Успішно пройшло валідацію: парсимо в типізовану структуру
			bytes, err := json.Marshal(current)
			if err != nil {
				return RateInput{}, fmt.Errorf("marshal repaired payload: %w", err)
			}
			var result RateInput
			if err := json.Unmarshal(bytes, &result); err != nil {
				return RateInput{}, fmt.Errorf("unmarshal to RateInput: %w", err)
			}
			return result, nil
		}

		if attempt < maxRetries {
			repaired, err := repairFn(current, lastErrs)
			if err != nil {
				return RateInput{}, fmt.Errorf("repair failed at attempt %d: %w", attempt+1, err)
			}
			current = repaired
		}
	}

	return RateInput{}, fmt.Errorf("validation failed after %d retries: %v", maxRetries, lastErrs)
}
