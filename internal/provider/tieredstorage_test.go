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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"

	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-altinity-clickhouse/internal/common"
)

func coldInstance(params string) *corev1alpha1.Instance {
	instance := newTestInstance("db", "ns", params)
	engine := instance.Spec.Components[common.ComponentEngine]
	engine.Storage = &corev1alpha1.Storage{Size: resource.MustParse("10Gi")}
	instance.Spec.Components[common.ComponentEngine] = engine
	return instance
}

func TestBuildCHIColdStoragePathStyle(t *testing.T) {
	bs := testBackupStorage("cold", "cold-creds", "lake", "http://minio:9000", "us-east-1", boolPtr(true), nil)
	c := newTestContext(t, coldInstance(`{"coldStorage":{"storageRef":"cold"}}`), bs)

	chi, err := buildCHI(c, 1)
	require.NoError(t, err)
	s := chi.Spec.Configuration.Settings

	disk := "storage_configuration/disks/s3_cold/"
	assert.Equal(t, "s3", s.Get(disk+"type").String())
	assert.Equal(t, "http://minio:9000/lake/ns/db/cold/", s.Get(disk+"endpoint").String())
	assert.Equal(t, "us-east-1", s.Get(disk+"region").String())
	for _, key := range []string{"access_key_id", "secret_access_key"} {
		require.NotNil(t, s.Get(disk+key))
		assert.True(t, s.Get(disk+key).IsSource(), "%s comes from the credentials Secret, not inlined", key)
	}

	cache := "storage_configuration/disks/s3_cold_cache/"
	assert.Equal(t, "cache", s.Get(cache+"type").String())
	assert.Equal(t, "s3_cold", s.Get(cache+"disk").String())
	assert.Equal(t, "/var/lib/clickhouse/s3_cold_cache/", s.Get(cache+"path").String(), "outside disks/, which the entrypoint would leave root-owned")
	assert.Equal(t, "1073741824", s.Get(cache+"max_size").String(), "10% of 10Gi")

	assert.Equal(t, "default", s.Get("storage_configuration/policies/tiered/volumes/hot/disk").String())
	assert.Equal(t, "s3_cold_cache", s.Get("storage_configuration/policies/tiered/volumes/cold/disk").String())
	assert.Nil(t, s.Get("storage_configuration/policies/default"), "the default policy is unchanged")
}

func TestBuildCHIColdStorageVirtualHostedAndCache(t *testing.T) {
	bs := testBackupStorage("cold", "cold-creds", "lake", "https://s3.ap-southeast-2.amazonaws.com", "ap-southeast-2", nil, nil)
	c := newTestContext(t, coldInstance(`{"coldStorage":{"storageRef":"cold","cachePercent":25}}`), bs)

	chi, err := buildCHI(c, 1)
	require.NoError(t, err)
	s := chi.Spec.Configuration.Settings
	assert.Equal(t, "https://lake.s3.ap-southeast-2.amazonaws.com/ns/db/cold/", s.Get("storage_configuration/disks/s3_cold/endpoint").String())
	assert.Equal(t, "2684354560", s.Get("storage_configuration/disks/s3_cold_cache/max_size").String(), "25% of 10Gi")
}

func TestBuildCHIWithoutColdStorage(t *testing.T) {
	chi, err := buildCHI(newTestContext(t, newTestInstance("db", "ns", "")), 1)
	require.NoError(t, err)
	assert.Nil(t, chi.Spec.Configuration.Settings.Get("storage_configuration/disks/s3_cold/type"))
}

func TestResolveColdStorageWaitsForBackupStorage(t *testing.T) {
	_, err := resolveColdStorage(newTestContext(t, coldInstance(`{"coldStorage":{"storageRef":"missing"}}`)))
	require.Error(t, err)
	assert.True(t, controller.IsWaitError(err), "a missing BackupStorage is waited for, not a failure")
}

func TestBackupConfigSetsObjectDiskPathWithColdStorage(t *testing.T) {
	bs := testBackupStorage("s3-storage", "s3-creds", "my-bucket", "https://s3.example.com", "us-east-1", nil, nil)
	creds := secretWith("s3-creds", map[string]string{"AWS_ACCESS_KEY_ID": "k", "AWS_SECRET_ACCESS_KEY": "s"})
	storages := corev1alpha1.InstanceBackupStorage{StorageRef: commonv1alpha1.ObjectRef{Name: "s3-storage"}}

	for _, tc := range []struct {
		params string
		want   string
	}{
		{params: "", want: ""},
		{params: `{"coldStorage":{"storageRef":"s3-storage"}}`, want: objectDiskPath},
	} {
		c := newTestContext(t, withBackup(coldInstance(tc.params), true, storages), bs, creds)
		require.NoError(t, ensureBackupInfra(c))
		var parsed backupStorageConfigFile
		require.NoError(t, yaml.Unmarshal(storageSecretIn(t, c).Data[BackupStorageConfigKey], &parsed))
		assert.Equal(t, tc.want, parsed.S3.ObjectDiskPath, "params %q", tc.params)
	}
}
