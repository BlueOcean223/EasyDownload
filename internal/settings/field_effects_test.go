package settings

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultFieldEffectRegistryMatchesSettingsSnapshot(t *testing.T) {
	typeOfSnapshot := reflect.TypeOf(SettingsSnapshot{})
	jsonFields := make(map[string]struct{}, typeOfSnapshot.NumField())
	for i := 0; i < typeOfSnapshot.NumField(); i++ {
		name := strings.Split(typeOfSnapshot.Field(i).Tag.Get("json"), ",")[0]
		require.NotEmpty(t, name)
		jsonFields[name] = struct{}{}
	}
	registry := DefaultFieldEffectRegistry()
	require.Len(t, registry, len(jsonFields))
	for field := range jsonFields {
		require.Contains(t, registry, field)
	}
}

func TestValidateChangedFieldsRejectsUnknownField(t *testing.T) {
	err := ValidateChangedFields(Changed{"newSetting": true}, DefaultFieldEffectRegistry())
	require.ErrorContains(t, err, "no declared effect class")
}
