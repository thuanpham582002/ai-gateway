// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package events

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequestEventSchemaMatchesStruct(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "kafka-request-event.schema.json"))
	require.NoError(t, err)

	var schema struct {
		Schema     string                     `json:"$schema"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	require.NoError(t, json.Unmarshal(raw, &schema))
	require.Equal(t, "https://json-schema.org/draft/2020-12/schema", schema.Schema)

	required := make(map[string]bool, len(schema.Required))
	for _, name := range schema.Required {
		required[name] = true
	}

	typeOfEvent := reflect.TypeFor[RequestEvent]()
	for i := range typeOfEvent.NumField() {
		field := typeOfEvent.Field(i)
		parts := strings.Split(field.Tag.Get("json"), ",")
		name := parts[0]
		require.NotEmpty(t, name, "RequestEvent.%s must declare a JSON name", field.Name)
		require.Contains(t, schema.Properties, name, "schema is missing RequestEvent.%s", field.Name)
		if len(parts) == 1 || parts[1] != "omitempty" {
			require.True(t, required[name], "schema must require RequestEvent.%s", field.Name)
		}
	}

	for name := range schema.Properties {
		found := false
		for i := range typeOfEvent.NumField() {
			jsonName := strings.Split(typeOfEvent.Field(i).Tag.Get("json"), ",")[0]
			if jsonName == name {
				found = true
				break
			}
		}
		require.True(t, found, "schema property %q has no RequestEvent field", name)
	}
}
