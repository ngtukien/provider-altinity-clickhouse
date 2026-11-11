// Copyright (C) 2026 The OpenEverest Contributors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package provider

import (
	"testing"

	chiv1 "github.com/altinity/clickhouse-operator/pkg/apis/clickhouse.altinity.com/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func settingValue(s *chiv1.Settings, name string) string {
	if v := s.Get(name); v != nil {
		return v.String()
	}
	return ""
}

// The default engine memory limit is 4Gi.
func TestBuildCHIDefaultQueryLimits(t *testing.T) {
	chi, err := buildCHI(newTestContext(t, newTestInstance("db", "ns", "")), 1)
	require.NoError(t, err)
	settings, profiles := chi.Spec.Configuration.Settings, chi.Spec.Configuration.Profiles

	assert.Equal(t, "3435973836", settingValue(settings, "max_server_memory_usage"), "80% of 4Gi")
	for _, p := range []string{"default", "readonly"} {
		assert.Equal(t, "2147483648", settingValue(profiles, p+"/max_memory_usage"), "half of 4Gi")
		assert.Equal(t, "1073741824", settingValue(profiles, p+"/max_bytes_before_external_group_by"))
		assert.Equal(t, "1073741824", settingValue(profiles, p+"/max_bytes_before_external_sort"))
	}
	assert.Nil(t, profiles.Get("default/max_execution_time"), "no time limit for the application user by default")
	assert.Equal(t, "2", settingValue(profiles, "readonly/readonly"))
	assert.Equal(t, "300", settingValue(profiles, "readonly/max_execution_time"))
	assert.Equal(t, "2147483648", settingValue(profiles, "readonly/constraints/max_memory_usage/max"))
	assert.Equal(t, "300", settingValue(profiles, "readonly/constraints/max_execution_time/max"))
}

func TestBuildCHICustomQueryLimits(t *testing.T) {
	instance := newTestInstance("db", "ns",
		`{"queryLimits":{"serverMemoryPercent":90,"maxMemoryUsage":"1Gi","maxExecutionTime":600,"readonlyMaxExecutionTime":60,"spillToDisk":"disabled"}}`)
	chi, err := buildCHI(newTestContext(t, instance), 1)
	require.NoError(t, err)
	settings, profiles := chi.Spec.Configuration.Settings, chi.Spec.Configuration.Profiles

	assert.Equal(t, "3865470566", settingValue(settings, "max_server_memory_usage"), "90% of 4Gi")
	assert.Equal(t, "1073741824", settingValue(profiles, "default/max_memory_usage"))
	assert.Equal(t, "600", settingValue(profiles, "default/max_execution_time"))
	assert.Equal(t, "60", settingValue(profiles, "readonly/max_execution_time"))
	assert.Equal(t, "60", settingValue(profiles, "readonly/constraints/max_execution_time/max"))
	assert.Nil(t, profiles.Get("default/max_bytes_before_external_group_by"), "spilling disabled")
	assert.Nil(t, profiles.Get("readonly/max_bytes_before_external_sort"), "spilling disabled")
}

func TestBuildCHIQueryLimitsKeepConfiguredServerMemory(t *testing.T) {
	for _, cfg := range []string{"max_server_memory_usage: 1000000000", "max_server_memory_usage_to_ram_ratio: 0.7"} {
		instance := newTestInstance("db", "ns", `{"configuration":"`+cfg+`"}`)
		chi, err := buildCHI(newTestContext(t, instance), 1)
		require.NoError(t, err)
		settings := chi.Spec.Configuration.Settings
		if cfg[len("max_server_memory_usage")] == ':' {
			assert.Equal(t, "1000000000", settingValue(settings, "max_server_memory_usage"), "the configured value wins")
		} else {
			assert.Nil(t, settings.Get("max_server_memory_usage"), "a configured ratio is not overridden by an absolute budget")
		}
	}
}
