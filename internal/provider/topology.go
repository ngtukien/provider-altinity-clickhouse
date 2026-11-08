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
	chiv1 "github.com/altinity/clickhouse-operator/pkg/apis/clickhouse.altinity.com/v1"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-altinity-clickhouse/definition/topologies/sharded"
	"github.com/openeverest/provider-altinity-clickhouse/internal/common"
)

// isSharded reports whether the Instance asks for the sharded topology.
func isSharded(c *controller.Context) bool {
	return c.Instance().GetTopologyType() == common.TopologySharded
}

// usesKeeper reports whether the Instance's topology runs a ClickHouse Keeper
// quorum: replicated and sharded do, standalone does not.
func usesKeeper(c *controller.Context) bool {
	switch c.Instance().GetTopologyType() {
	case common.TopologyReplicated, common.TopologySharded:
		return true
	}
	return false
}

// shardsCount returns the number of shards: the sharded topology's
// parameter (default common.DefaultShardsCount), and 1 for every other
// topology.
func shardsCount(c *controller.Context) int {
	if !isSharded(c) {
		return 1
	}
	var params sharded.ShardedTopologyParameters
	if c.TryDecodeTopologyParameters(&params) && params.Shards > 0 {
		return int(params.Shards)
	}
	return common.DefaultShardsCount
}

// liveShardsCount returns the shard count of the deployed cluster, 1 when the
// layout does not say.
func liveShardsCount(chi *chiv1.ClickHouseInstallation) int {
	if chi.Spec.Configuration == nil {
		return 1
	}
	for _, cluster := range chi.Spec.Configuration.Clusters {
		if cluster == nil || cluster.Name != common.CHIClusterName || cluster.Layout == nil {
			continue
		}
		if cluster.Layout.ShardsCount > 0 {
			return cluster.Layout.ShardsCount
		}
	}
	return 1
}
