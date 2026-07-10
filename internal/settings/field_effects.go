package settings

import (
	"fmt"
	"strings"
)

// FieldEffectClass is the declared runtime behavior of a user setting.
type FieldEffectClass string

const (
	FieldEffectPure       FieldEffectClass = "pure_persisted"
	FieldEffectCritical   FieldEffectClass = "critical_reversible"
	FieldEffectDeferred   FieldEffectClass = "deferred_restart"
	FieldEffectBestEffort FieldEffectClass = "best_effort"
	FieldEffectDynamic    FieldEffectClass = "dynamic_deferred"
)

// FieldEffectRegistry is exhaustive for SettingsSnapshot JSON fields. App's
// plan builder validates every Changed key against it and rejects unclassified
// fields instead of silently treating them as pure persistence.
type FieldEffectRegistry map[string]FieldEffectClass

var defaultFieldEffectRegistry = FieldEffectRegistry{
	"proxyPort":            FieldEffectDynamic,
	"apiPort":              FieldEffectDeferred,
	"downloadDir":          FieldEffectCritical,
	"maxConcurrent":        FieldEffectCritical,
	"minimizeToTray":       FieldEffectBestEffort,
	"showNotification":     FieldEffectBestEffort,
	"firstRunComplete":     FieldEffectPure,
	"closeAction":          FieldEffectPure,
	"dontAskOnClose":       FieldEffectPure,
	"theme":                FieldEffectBestEffort,
	"language":             FieldEffectBestEffort,
	"upstreamProxy":        FieldEffectCritical,
	"useUpstreamProxy":     FieldEffectCritical,
	"proxyDebug":           FieldEffectCritical,
	"dontRemindCertWizard": FieldEffectPure,
}

func DefaultFieldEffectRegistry() FieldEffectRegistry {
	result := make(FieldEffectRegistry, len(defaultFieldEffectRegistry))
	for field, class := range defaultFieldEffectRegistry {
		result[field] = class
	}
	return result
}

func ValidateChangedFields(changed Changed, registry FieldEffectRegistry) error {
	for field, changedValue := range changed {
		if !changedValue {
			continue
		}
		class, exists := registry[field]
		if !exists {
			return fmt.Errorf("settings field %q has no declared effect class", field)
		}
		switch class {
		case FieldEffectPure, FieldEffectCritical, FieldEffectDeferred, FieldEffectBestEffort, FieldEffectDynamic:
		default:
			return fmt.Errorf("settings field %q has invalid effect class %q", field, strings.TrimSpace(string(class)))
		}
	}
	return nil
}

func RequiresSpecificPlannerEffect(class FieldEffectClass) bool {
	return class == FieldEffectCritical || class == FieldEffectDeferred || class == FieldEffectDynamic
}
