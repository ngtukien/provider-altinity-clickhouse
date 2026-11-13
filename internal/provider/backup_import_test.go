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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
)

func TestImportedBackups(t *testing.T) {
	imp := &backupv1alpha1.BackupImport{
		ObjectMeta: metav1.ObjectMeta{Name: "imp", Namespace: "ch"},
		Spec: backupv1alpha1.BackupImportSpec{
			ClassRef:   commonv1alpha1.ObjectRef{Name: "altinity-clickhouse-backup"},
			StorageRef: commonv1alpha1.ObjectRef{Name: "lab-s3"},
		},
	}
	t0 := time.Date(2026, 11, 13, 2, 30, 0, 0, time.UTC)
	objects := []storedObject{
		// A sharded backup: one metadata.json per shard, merged.
		{Key: "backup/ch/sharded/clickhouse/0/dr-1/metadata.json", LastModified: t0},
		{Key: "backup/ch/sharded/clickhouse/1/dr-1/metadata.json", LastModified: t0.Add(time.Minute)},
		{Key: "backup/ch/sharded/clickhouse/0/dr-1/shadow/default/t/part.tar", LastModified: t0},
		// The same backup name on another installation.
		{Key: "backup/ch/other/clickhouse/0/dr-1/metadata.json", LastModified: t0},
		// Not a backup: an incomplete upload, cold data, a malformed key.
		{Key: "backup/ch/sharded/clickhouse/0/half/shadow/x", LastModified: t0},
		{Key: "ch/sharded/cold/abc", LastModified: t0},
		{Key: "backup/ch//clickhouse/0/x/metadata.json", LastModified: t0},
		// Another namespace's backup in a shared bucket: never imported.
		{Key: "backup/other-tenant/db/clickhouse/0/secret/metadata.json", LastModified: t0},
	}

	backups := importedBackups(imp, objects)
	require.Len(t, backups, 2)
	other, sharded := backups[0], backups[1]

	assert.Equal(t, "backup/ch/sharded/clickhouse/dr-1", sharded.Spec.Origin.External.Path)
	assert.Equal(t, t0, sharded.Spec.Origin.External.StartedAt.UTC())
	assert.Equal(t, t0.Add(time.Minute), sharded.Spec.Origin.External.CompletedAt.UTC())
	assert.Equal(t, backupv1alpha1.BackupOriginTypeExternal, sharded.Spec.Origin.Type)
	assert.Equal(t, backupv1alpha1.BackupDeletionPolicyRetain, sharded.Spec.DeletionPolicy)
	assert.Equal(t, "lab-s3", sharded.Spec.StorageRef.Name)
	assert.Equal(t, "altinity-clickhouse-backup", sharded.Spec.ClassRef.Name)

	assert.Equal(t, "backup/ch/other/clickhouse/dr-1", other.Spec.Origin.External.Path)
	assert.NotEqual(t, other.Name, sharded.Name, "same backup name on two installations")
	assert.Equal(t, sharded.Name, importedBackups(imp, objects)[1].Name, "names are deterministic")

	ns, installation, name, ok := importedSource(sharded.Spec.Origin.External.Path)
	require.True(t, ok)
	assert.Equal(t, []string{"ch", "sharded", "dr-1"}, []string{ns, installation, name})
}

func TestImportedBackupName(t *testing.T) {
	for _, path := range []string{
		"backup/ch/i/clickhouse/ch-demo-t-reg-t-reg-every5m-20261113-022500",
		"backup/ch/i/clickhouse/Upper_Case.and.dots",
		"backup/ch/i/clickhouse/a-very-long-backup-name-that-goes-on-and-on-past-the-fifty-characters",
	} {
		name := importedBackupName(path)
		assert.Empty(t, validation.IsDNS1123Subdomain(name), "%q from %q", name, path)
		assert.LessOrEqual(t, len(name), 63)
	}
}
