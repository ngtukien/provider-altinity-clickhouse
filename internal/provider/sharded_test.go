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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"

	chiv1 "github.com/altinity/clickhouse-operator/pkg/apis/clickhouse.altinity.com/v1"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"

	"github.com/openeverest/provider-altinity-clickhouse/internal/common"
)

func newShardedTestInstance(shards int, replicas *int32) *corev1alpha1.Instance {
	instance := newTestInstance("db", "ns", "")
	instance.Spec.Topology = &corev1alpha1.TopologySpec{Type: common.TopologySharded}
	if shards != 0 {
		instance.Spec.Topology.Parameters = &runtime.RawExtension{Raw: []byte(fmt.Sprintf(`{"shards":%d}`, shards))}
	}
	if replicas != nil {
		engine := instance.Spec.Components[common.ComponentEngine]
		engine.Replicas = replicas
		instance.Spec.Components[common.ComponentEngine] = engine
	}
	return instance
}

func mustBuildShardedCHI(t *testing.T, instance *corev1alpha1.Instance) *chiv1.ClickHouseInstallation {
	t.Helper()
	c := newTestContext(t, instance)
	chi, err := buildCHI(c, replicasCount(c))
	require.NoError(t, err)
	return chi
}

func layoutOf(chi *chiv1.ClickHouseInstallation) *chiv1.ChiClusterLayout {
	return chi.Spec.Configuration.Clusters[0].Layout
}

func TestShardedLayout(t *testing.T) {
	// Defaults: 2 shards x 1 replica.
	chi := mustBuildShardedCHI(t, newShardedTestInstance(0, nil))
	assert.Equal(t, common.DefaultShardsCount, layoutOf(chi).ShardsCount)
	assert.Equal(t, 1, layoutOf(chi).ReplicasCount)
	assert.NotNil(t, chi.Spec.Configuration.Zookeeper, "sharded ClickHouse is wired to Keeper")

	three := int32(2)
	chi = mustBuildShardedCHI(t, newShardedTestInstance(3, &three))
	assert.Equal(t, 3, layoutOf(chi).ShardsCount)
	assert.Equal(t, 2, layoutOf(chi).ReplicasCount)

	// Other topologies keep a single shard.
	chi = mustBuildShardedCHI(t, newReplicatedTestInstance(""))
	assert.Equal(t, 1, layoutOf(chi).ShardsCount)
}

func TestValidateShards(t *testing.T) {
	assert.NoError(t, validate(t, newShardedTestInstance(2, nil)))
	assert.ErrorContains(t, validate(t, newShardedTestInstance(1, nil)), "at least 2 shards")

	zero := int32(0)
	assert.ErrorContains(t, validate(t, newShardedTestInstance(2, &zero)), "at least 1 replica per shard")

	standalone := newTestInstance("db", "ns", "")
	standalone.Spec.Topology = &corev1alpha1.TopologySpec{
		Type:       common.TopologyStandalone,
		Parameters: &runtime.RawExtension{Raw: []byte(`{"shards":2}`)},
	}
	assert.ErrorContains(t, validate(t, standalone), "only used by the sharded topology")
}

func TestValidateShardTransition(t *testing.T) {
	live := mustBuildShardedCHI(t, newShardedTestInstance(3, nil))

	assert.NoError(t, validate(t, newShardedTestInstance(3, nil), live))
	assert.NoError(t, validate(t, newShardedTestInstance(4, nil), live), "adding shards is allowed")
	assert.ErrorContains(t, validate(t, newShardedTestInstance(2, nil), live), "cannot be decreased from 3 to 2")
	assert.ErrorContains(t, validate(t, newReplicatedTestInstance(""), live), "cannot be changed from sharded")

	replicatedLive := mustBuildShardedCHI(t, newReplicatedTestInstance(""))
	assert.ErrorContains(t, validate(t, newShardedTestInstance(2, nil), replicatedLive), "cannot be changed to sharded")
}

func TestReplicaServiceNamesCoverEveryShard(t *testing.T) {
	assert.Equal(t, []string{
		"chi-db-clickhouse-0-0", "chi-db-clickhouse-0-1",
		"chi-db-clickhouse-1-0", "chi-db-clickhouse-1-1",
	}, replicaServiceNames("db", 2, 2))
}
