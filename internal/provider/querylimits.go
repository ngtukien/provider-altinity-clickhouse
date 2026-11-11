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
	"fmt"
	"slices"
	"strconv"
	"strings"

	chiv1 "github.com/altinity/clickhouse-operator/pkg/apis/clickhouse.altinity.com/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/openeverest/provider-altinity-clickhouse/definition/components"
)

const (
	defaultServerMemoryPercent      = 80
	defaultReadonlyMaxExecutionTime = 300
	readonlyProfile                 = "readonly"
	spillDisabled                   = "disabled"
)

var spillModes = []string{"enabled", spillDisabled}

// queryLimits are the resolved limits, in bytes and seconds.
type queryLimits struct {
	serverMemory             int64
	maxMemoryUsage           int64
	maxExecutionTime         int
	readonlyMaxExecutionTime int
	spill                    bool
}

// resolveQueryLimits fills the defaults in from the engine's memory limit.
// The parameters are expected to have passed validateQueryLimits.
func resolveQueryLimits(p *components.QueryLimitsParameters, memoryLimit resource.Quantity) queryLimits {
	if p == nil {
		p = &components.QueryLimitsParameters{}
	}
	percent := p.ServerMemoryPercent
	if percent == 0 {
		percent = defaultServerMemoryPercent
	}
	limits := queryLimits{
		serverMemory:             memoryLimit.Value() * int64(percent) / 100,
		maxMemoryUsage:           memoryLimit.Value() / 2,
		maxExecutionTime:         p.MaxExecutionTime,
		readonlyMaxExecutionTime: p.ReadonlyMaxExecutionTime,
		spill:                    !strings.EqualFold(p.SpillToDisk, spillDisabled),
	}
	if p.MaxMemoryUsage != "" {
		if q, err := resource.ParseQuantity(p.MaxMemoryUsage); err == nil {
			limits.maxMemoryUsage = q.Value()
		}
	}
	if limits.readonlyMaxExecutionTime == 0 {
		limits.readonlyMaxExecutionTime = defaultReadonlyMaxExecutionTime
	}
	return limits
}

// applyQueryLimits sets the server memory budget, unless the engine
// configuration already sets one, and builds the settings profiles.
func applyQueryLimits(settings *chiv1.Settings, limits queryLimits) *chiv1.Settings {
	if !settings.Has("max_server_memory_usage") && !settings.Has("max_server_memory_usage_to_ram_ratio") {
		settings.Set("max_server_memory_usage", scalar(limits.serverMemory))
	}

	profiles := chiv1.NewSettings()
	for _, profile := range []string{"default", readonlyProfile} {
		profiles.Set(profile+"/max_memory_usage", scalar(limits.maxMemoryUsage))
		if limits.spill {
			profiles.Set(profile+"/max_bytes_before_external_group_by", scalar(limits.maxMemoryUsage/2))
			profiles.Set(profile+"/max_bytes_before_external_sort", scalar(limits.maxMemoryUsage/2))
		}
	}
	if limits.maxExecutionTime > 0 {
		profiles.Set("default/max_execution_time", scalar(int64(limits.maxExecutionTime)))
	}

	// readonly=2 lets BI drivers send SET on connect (readonly=1 makes them
	// fail); the constraints keep those users from raising their own limits.
	profiles.Set(readonlyProfile+"/readonly", chiv1.NewSettingScalar("2"))
	profiles.Set(readonlyProfile+"/max_execution_time", scalar(int64(limits.readonlyMaxExecutionTime)))
	profiles.Set(readonlyProfile+"/constraints/max_memory_usage/max", scalar(limits.maxMemoryUsage))
	profiles.Set(readonlyProfile+"/constraints/max_execution_time/max", scalar(int64(limits.readonlyMaxExecutionTime)))
	return profiles
}

func scalar(v int64) *chiv1.Setting { return chiv1.NewSettingScalar(strconv.FormatInt(v, 10)) }

// validateQueryLimits rejects limits that cannot be applied to an engine with
// this memory limit.
func validateQueryLimits(path *field.Path, p *components.QueryLimitsParameters, memoryLimit resource.Quantity) field.ErrorList {
	if p == nil {
		return nil
	}
	var errs field.ErrorList
	if p.ServerMemoryPercent != 0 && (p.ServerMemoryPercent < 10 || p.ServerMemoryPercent > 95) {
		errs = append(errs, field.Invalid(path.Child("serverMemoryPercent"), p.ServerMemoryPercent, "must be between 10 and 95"))
	}
	if p.MaxExecutionTime < 0 {
		errs = append(errs, field.Invalid(path.Child("maxExecutionTime"), p.MaxExecutionTime, "must not be negative"))
	}
	if p.ReadonlyMaxExecutionTime < 0 {
		errs = append(errs, field.Invalid(path.Child("readonlyMaxExecutionTime"), p.ReadonlyMaxExecutionTime, "must not be negative"))
	}
	if p.SpillToDisk != "" && !slices.ContainsFunc(spillModes, func(m string) bool { return strings.EqualFold(m, p.SpillToDisk) }) {
		errs = append(errs, field.NotSupported(path.Child("spillToDisk"), p.SpillToDisk, spillModes))
	}
	if p.MaxMemoryUsage != "" {
		q, err := resource.ParseQuantity(p.MaxMemoryUsage)
		switch {
		case err != nil:
			errs = append(errs, field.Invalid(path.Child("maxMemoryUsage"), p.MaxMemoryUsage, "must be a quantity such as 2Gi"))
		case q.Sign() <= 0:
			errs = append(errs, field.Invalid(path.Child("maxMemoryUsage"), p.MaxMemoryUsage, "must be positive"))
		case len(errs) == 0:
			if budget := resolveQueryLimits(p, memoryLimit).serverMemory; q.Value() > budget {
				errs = append(errs, field.Invalid(path.Child("maxMemoryUsage"), p.MaxMemoryUsage,
					fmt.Sprintf("must not exceed the server memory budget %s (serverMemoryPercent of the memory limit %s)",
						resource.NewQuantity(budget, resource.BinarySI), memoryLimit.String())))
			}
		}
	}
	return errs
}
