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
	"context"
	"testing"
	"time"

	chiv1 "github.com/altinity/clickhouse-operator/pkg/apis/clickhouse.altinity.com/v1"
	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func newRestoreTestContext(t *testing.T, instance *corev1alpha1.Instance, objs ...client.Object) *controller.Context {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))
	require.NoError(t, backupv1alpha1.AddToScheme(scheme))
	require.NoError(t, chiv1.AddToScheme(scheme))

	all := append([]client.Object{instance, testProvider(t)}, objs...)
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(all...).
		WithStatusSubresource(&chiv1.ClickHouseRestore{}, &backupv1alpha1.Restore{}, &corev1alpha1.Instance{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				t := obj.GetCreationTimestamp()
				if t.IsZero() {
					obj.SetCreationTimestamp(metav1.Now())
				}
				return client.Create(ctx, obj, opts...)
			},
		}).
		Build()

	return controller.NewContext(context.Background(), fakeClient, instance, "altinity-clickhouse")
}

func testSuccessfulBackup(name, ns, instanceName, storageName string) *backupv1alpha1.Backup {
	now := metav1.Now()
	return &backupv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: backupv1alpha1.BackupSpec{
			Origin: backupv1alpha1.BackupOrigin{
				Type: backupv1alpha1.BackupOriginTypeInstance,
				InstanceRef: &commonv1alpha1.ObjectRef{
					Name: instanceName,
				},
			},
			StorageRef: commonv1alpha1.ObjectRef{
				Name: storageName,
			},
		},
		Status: backupv1alpha1.BackupStatus{
			State:       backupv1alpha1.BackupStateSucceeded,
			StartedAt:   &now,
			CompletedAt: &now,
			OperatorBackupRef: &commonv1alpha1.TypedObjectRef{
				Group: "clickhouse.altinity.com",
				Kind:  "ClickHouseBackup",
				Name:  name,
			},
		},
	}
}

func testTargetCHI(name, ns string) *chiv1.ClickHouseInstallation {
	return &chiv1.ClickHouseInstallation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
	}
}

func TestSyncRestore_Validation(t *testing.T) {
	p := &Provider{}
	ns := "default"
	instName := "test-ch"
	instance := testInstanceWithStorage(instName, ns, "s3-storage")
	chi := testTargetCHI(instName, ns)

	t.Run("PointInTime rejected", func(t *testing.T) {
		restore := &backupv1alpha1.Restore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "restore-pitr",
				Namespace: ns,
			},
			Spec: backupv1alpha1.RestoreSpec{
				InstanceRef: commonv1alpha1.ObjectRef{Name: instName},
				DataSource: backupv1alpha1.DataSource{
					Type: backupv1alpha1.DataSourceTypePointInTime,
				},
			},
		}

		c := newRestoreTestContext(t, instance, chi, restore)
		status, err := p.SyncRestore(c, restore)
		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.RestoreStateFailed, status.State)
		assert.Contains(t, status.Message, "point-in-time restore is not supported")
	})

	t.Run("missing backup", func(t *testing.T) {
		restore := &backupv1alpha1.Restore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "restore-missing-bk",
				Namespace: ns,
			},
			Spec: backupv1alpha1.RestoreSpec{
				InstanceRef: commonv1alpha1.ObjectRef{Name: instName},
				DataSource: backupv1alpha1.DataSource{
					Type: backupv1alpha1.DataSourceTypeBackup,
					Backup: &backupv1alpha1.DataSourceBackup{
						BackupRef: commonv1alpha1.ObjectRef{Name: "non-existent-backup"},
					},
				},
			},
		}

		c := newRestoreTestContext(t, instance, chi, restore)
		status, err := p.SyncRestore(c, restore)
		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.RestoreStateFailed, status.State)
		assert.Contains(t, status.Message, `Backup "non-existent-backup" not found`)
	})

	externalRestore := func(t *testing.T, path string) (backupv1alpha1.RestoreState, string, *controller.Context) {
		t.Helper()
		backup := &backupv1alpha1.Backup{
			ObjectMeta: metav1.ObjectMeta{Name: "imported", Namespace: ns},
			Spec: backupv1alpha1.BackupSpec{
				Origin: backupv1alpha1.BackupOrigin{
					Type:     backupv1alpha1.BackupOriginTypeExternal,
					External: &backupv1alpha1.BackupOriginExternal{Path: path},
				},
				StorageRef: commonv1alpha1.ObjectRef{Name: "s3-storage"},
			},
			Status: backupv1alpha1.BackupStatus{State: backupv1alpha1.BackupStateSucceeded},
		}
		restore := &backupv1alpha1.Restore{
			ObjectMeta: metav1.ObjectMeta{Name: "restore-external", Namespace: ns},
			Spec: backupv1alpha1.RestoreSpec{
				InstanceRef: commonv1alpha1.ObjectRef{Name: instName},
				DataSource: backupv1alpha1.DataSource{
					Type:   backupv1alpha1.DataSourceTypeBackup,
					Backup: &backupv1alpha1.DataSourceBackup{BackupRef: commonv1alpha1.ObjectRef{Name: backup.Name}},
				},
			},
		}
		c := newRestoreTestContext(t, instance, chi, backup, restore)
		status, err := p.SyncRestore(c, restore)
		require.NoError(t, err)
		return status.State, status.Message, c
	}

	t.Run("imported backup with an unknown path", func(t *testing.T) {
		state, message, _ := externalRestore(t, "/some/path")
		assert.Equal(t, backupv1alpha1.RestoreStateFailed, state)
		assert.Contains(t, message, "unknown path")
	})

	// Its path names the source: the restore reads that installation's backup.
	t.Run("imported backup restores from its source", func(t *testing.T) {
		_, _, c := externalRestore(t, "backup/other-ns/old-ch/clickhouse/dr-1")
		chr := &chiv1.ClickHouseRestore{}
		require.NoError(t, c.Get(chr, "restore-external"))
		assert.Equal(t, "dr-1", chr.Spec.BackupName)
		assert.Equal(t, "old-ch", chr.Spec.SourceInstallation)
		assert.Equal(t, "other-ns", chr.Spec.SourceNamespace)
	})

	t.Run("imported backup of the same Instance", func(t *testing.T) {
		_, _, c := externalRestore(t, "backup/"+ns+"/"+instName+"/clickhouse/dr-1")
		chr := &chiv1.ClickHouseRestore{}
		require.NoError(t, c.Get(chr, "restore-external"))
		assert.Equal(t, "dr-1", chr.Spec.BackupName)
		assert.Empty(t, chr.Spec.SourceInstallation)
		assert.Empty(t, chr.Spec.SourceNamespace)
	})

	t.Run("backup pending", func(t *testing.T) {
		backup := testSuccessfulBackup("pending-backup", ns, instName, "s3-storage")
		backup.Status.State = backupv1alpha1.BackupStateRunning

		restore := &backupv1alpha1.Restore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "restore-bk-pending",
				Namespace: ns,
			},
			Spec: backupv1alpha1.RestoreSpec{
				InstanceRef: commonv1alpha1.ObjectRef{Name: instName},
				DataSource: backupv1alpha1.DataSource{
					Type: backupv1alpha1.DataSourceTypeBackup,
					Backup: &backupv1alpha1.DataSourceBackup{
						BackupRef: commonv1alpha1.ObjectRef{Name: backup.Name},
					},
				},
			},
		}

		c := newRestoreTestContext(t, instance, chi, backup, restore)
		status, err := p.SyncRestore(c, restore)
		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.RestoreStatePending, status.State)
		assert.Contains(t, status.Message, `waiting for Backup "pending-backup" to succeed`)
	})

	t.Run("backup failed", func(t *testing.T) {
		backup := testSuccessfulBackup("failed-backup", ns, instName, "s3-storage")
		backup.Status.State = backupv1alpha1.BackupStateFailed

		restore := &backupv1alpha1.Restore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "restore-bk-failed",
				Namespace: ns,
			},
			Spec: backupv1alpha1.RestoreSpec{
				InstanceRef: commonv1alpha1.ObjectRef{Name: instName},
				DataSource: backupv1alpha1.DataSource{
					Type: backupv1alpha1.DataSourceTypeBackup,
					Backup: &backupv1alpha1.DataSourceBackup{
						BackupRef: commonv1alpha1.ObjectRef{Name: backup.Name},
					},
				},
			},
		}

		c := newRestoreTestContext(t, instance, chi, backup, restore)
		status, err := p.SyncRestore(c, restore)
		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.RestoreStateFailed, status.State)
		assert.Contains(t, status.Message, `source Backup "failed-backup" has failed`)
	})

	t.Run("storage not on target", func(t *testing.T) {
		backup := testSuccessfulBackup("storage-backup", ns, instName, "unregistered-storage")

		restore := &backupv1alpha1.Restore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "restore-storage-missing",
				Namespace: ns,
			},
			Spec: backupv1alpha1.RestoreSpec{
				InstanceRef: commonv1alpha1.ObjectRef{Name: instName},
				DataSource: backupv1alpha1.DataSource{
					Type: backupv1alpha1.DataSourceTypeBackup,
					Backup: &backupv1alpha1.DataSourceBackup{
						BackupRef: commonv1alpha1.ObjectRef{Name: backup.Name},
					},
				},
			},
		}

		c := newRestoreTestContext(t, instance, chi, backup, restore)
		status, err := p.SyncRestore(c, restore)
		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.RestoreStateFailed, status.State)
		assert.Contains(t, status.Message, `storage "unregistered-storage" is not registered and enabled on Instance "test-ch"`)
	})

	t.Run("CHI missing", func(t *testing.T) {
		backup := testSuccessfulBackup("chi-missing-backup", ns, instName, "s3-storage")

		restore := &backupv1alpha1.Restore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "restore-chi-missing",
				Namespace: ns,
			},
			Spec: backupv1alpha1.RestoreSpec{
				InstanceRef: commonv1alpha1.ObjectRef{Name: instName},
				DataSource: backupv1alpha1.DataSource{
					Type: backupv1alpha1.DataSourceTypeBackup,
					Backup: &backupv1alpha1.DataSourceBackup{
						BackupRef: commonv1alpha1.ObjectRef{Name: backup.Name},
					},
				},
			},
		}

		c := newRestoreTestContext(t, instance, backup, restore) // CHI omitted
		status, err := p.SyncRestore(c, restore)
		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.RestoreStatePending, status.State)
		assert.Equal(t, "Waiting for ClickHouseInstallation", status.Message)
	})

	t.Run("invalid parameters", func(t *testing.T) {
		backup := testSuccessfulBackup("invalid-params-backup", ns, instName, "s3-storage")

		restore := &backupv1alpha1.Restore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "restore-invalid-params",
				Namespace: ns,
			},
			Spec: backupv1alpha1.RestoreSpec{
				InstanceRef: commonv1alpha1.ObjectRef{Name: instName},
				DataSource: backupv1alpha1.DataSource{
					Type: backupv1alpha1.DataSourceTypeBackup,
					Backup: &backupv1alpha1.DataSourceBackup{
						BackupRef: commonv1alpha1.ObjectRef{Name: backup.Name},
					},
				},
				Parameters: &runtime.RawExtension{
					Raw: []byte(`{"overwrite":"not-a-bool"}`),
				},
			},
		}

		c := newRestoreTestContext(t, instance, chi, backup, restore)
		status, err := p.SyncRestore(c, restore)
		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.RestoreStateFailed, status.State)
		assert.Contains(t, status.Message, "invalid restore parameters")
	})
}

func TestSyncRestore_CreationSpec(t *testing.T) {
	p := &Provider{}
	ns := "default"
	instName := "test-ch"
	instance := testInstanceWithStorage(instName, ns, "s3-storage")
	chi := testTargetCHI(instName, ns)

	t.Run("same instance -> no SourceInstallation", func(t *testing.T) {
		backup := testSuccessfulBackup("same-inst-backup", ns, instName, "s3-storage")

		restore := &backupv1alpha1.Restore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "restore-same-inst",
				Namespace: ns,
			},
			Spec: backupv1alpha1.RestoreSpec{
				InstanceRef: commonv1alpha1.ObjectRef{Name: instName},
				DataSource: backupv1alpha1.DataSource{
					Type: backupv1alpha1.DataSourceTypeBackup,
					Backup: &backupv1alpha1.DataSourceBackup{
						BackupRef: commonv1alpha1.ObjectRef{Name: backup.Name},
					},
				},
			},
		}

		c := newRestoreTestContext(t, instance, chi, backup, restore)
		status, err := p.SyncRestore(c, restore)
		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.RestoreStatePending, status.State)

		chr := &chiv1.ClickHouseRestore{}
		err = c.Client().Get(context.Background(), client.ObjectKey{Namespace: ns, Name: restore.Name}, chr)
		require.NoError(t, err)
		assert.Equal(t, instName, chr.Spec.ClickHouseInstallation)
		assert.Equal(t, backup.Name, chr.Spec.BackupName)
		assert.Empty(t, chr.Spec.SourceInstallation)
		assert.Nil(t, chr.Spec.Overwrite)
		assert.Nil(t, chr.Spec.SchemaOnly)
		assert.Empty(t, chr.Spec.Tables)

		// Controller reference verification
		require.Len(t, chr.OwnerReferences, 1)
		assert.Equal(t, restore.Name, chr.OwnerReferences[0].Name)
		assert.True(t, *chr.OwnerReferences[0].Controller)
	})

	t.Run("other instance -> SourceInstallation set", func(t *testing.T) {
		backup := testSuccessfulBackup("other-inst-backup", ns, "source-ch", "s3-storage")

		restore := &backupv1alpha1.Restore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "restore-other-inst",
				Namespace: ns,
			},
			Spec: backupv1alpha1.RestoreSpec{
				InstanceRef: commonv1alpha1.ObjectRef{Name: instName},
				DataSource: backupv1alpha1.DataSource{
					Type: backupv1alpha1.DataSourceTypeBackup,
					Backup: &backupv1alpha1.DataSourceBackup{
						BackupRef: commonv1alpha1.ObjectRef{Name: backup.Name},
					},
				},
			},
		}

		c := newRestoreTestContext(t, instance, chi, backup, restore)
		status, err := p.SyncRestore(c, restore)
		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.RestoreStatePending, status.State)

		chr := &chiv1.ClickHouseRestore{}
		err = c.Client().Get(context.Background(), client.ObjectKey{Namespace: ns, Name: restore.Name}, chr)
		require.NoError(t, err)
		assert.Equal(t, instName, chr.Spec.ClickHouseInstallation)
		assert.Equal(t, "source-ch", chr.Spec.SourceInstallation)
	})

	t.Run("parameters passed through", func(t *testing.T) {
		backup := testSuccessfulBackup("params-backup", ns, instName, "s3-storage")

		restore := &backupv1alpha1.Restore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "restore-with-params",
				Namespace: ns,
			},
			Spec: backupv1alpha1.RestoreSpec{
				InstanceRef: commonv1alpha1.ObjectRef{Name: instName},
				DataSource: backupv1alpha1.DataSource{
					Type: backupv1alpha1.DataSourceTypeBackup,
					Backup: &backupv1alpha1.DataSourceBackup{
						BackupRef: commonv1alpha1.ObjectRef{Name: backup.Name},
					},
				},
				Parameters: &runtime.RawExtension{
					Raw: []byte(`{"overwrite":true,"schemaOnly":true,"tables":"default.test_table"}`),
				},
			},
		}

		c := newRestoreTestContext(t, instance, chi, backup, restore)
		status, err := p.SyncRestore(c, restore)
		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.RestoreStatePending, status.State)

		chr := &chiv1.ClickHouseRestore{}
		err = c.Client().Get(context.Background(), client.ObjectKey{Namespace: ns, Name: restore.Name}, chr)
		require.NoError(t, err)
		require.NotNil(t, chr.Spec.Overwrite)
		assert.True(t, chr.Spec.Overwrite.IsTrue())
		require.NotNil(t, chr.Spec.SchemaOnly)
		assert.True(t, chr.Spec.SchemaOnly.IsTrue())
		assert.Equal(t, "default.test_table", chr.Spec.Tables)
	})

	t.Run("scheduled backup with remote-name annotation", func(t *testing.T) {
		backup := testSuccessfulBackup("scheduled-backup", ns, instName, "s3-storage")
		backup.Annotations = map[string]string{
			"provider.openeverest.io/remote-backup-name": "remote-scheduled-20261111",
		}

		restore := &backupv1alpha1.Restore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "restore-remote-name",
				Namespace: ns,
			},
			Spec: backupv1alpha1.RestoreSpec{
				InstanceRef: commonv1alpha1.ObjectRef{Name: instName},
				DataSource: backupv1alpha1.DataSource{
					Type: backupv1alpha1.DataSourceTypeBackup,
					Backup: &backupv1alpha1.DataSourceBackup{
						BackupRef: commonv1alpha1.ObjectRef{Name: backup.Name},
					},
				},
			},
		}

		c := newRestoreTestContext(t, instance, chi, backup, restore)
		status, err := p.SyncRestore(c, restore)
		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.RestoreStatePending, status.State)

		chr := &chiv1.ClickHouseRestore{}
		err = c.Client().Get(context.Background(), client.ObjectKey{Namespace: ns, Name: restore.Name}, chr)
		require.NoError(t, err)
		assert.Equal(t, "remote-scheduled-20261111", chr.Spec.BackupName)
	})
}

func TestSyncRestore_PhaseMapping(t *testing.T) {
	p := &Provider{}
	ns := "default"
	instName := "test-ch"
	instance := testInstanceWithStorage(instName, ns, "s3-storage")
	chi := testTargetCHI(instName, ns)
	backup := testSuccessfulBackup("phase-backup", ns, instName, "s3-storage")

	started := metav1.NewTime(time.Now().Add(-10 * time.Minute).Truncate(time.Second))
	completed := metav1.NewTime(time.Now().Truncate(time.Second))

	baseRestore := func(name string) *backupv1alpha1.Restore {
		return &backupv1alpha1.Restore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
			Spec: backupv1alpha1.RestoreSpec{
				InstanceRef: commonv1alpha1.ObjectRef{Name: instName},
				DataSource: backupv1alpha1.DataSource{
					Type: backupv1alpha1.DataSourceTypeBackup,
					Backup: &backupv1alpha1.DataSourceBackup{
						BackupRef: commonv1alpha1.ObjectRef{Name: backup.Name},
					},
				},
			},
		}
	}

	t.Run("Completed -> Succeeded", func(t *testing.T) {
		restore := baseRestore("res-completed")
		chr := &chiv1.ClickHouseRestore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      restore.Name,
				Namespace: ns,
			},
			Status: chiv1.ClickHouseRestoreStatus{
				Phase:          chiv1.OperationPhaseCompleted,
				StartTime:      &started,
				CompletionTime: &completed,
			},
		}

		c := newRestoreTestContext(t, instance, chi, backup, restore, chr)
		status, err := p.SyncRestore(c, restore)
		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.RestoreStateSucceeded, status.State)
		assert.Equal(t, &started, status.StartedAt)
		assert.Equal(t, &completed, status.CompletedAt)
		require.NotNil(t, status.OperatorRestoreRef)
		assert.Equal(t, "ClickHouseRestore", status.OperatorRestoreRef.Kind)
		assert.Equal(t, restore.Name, status.OperatorRestoreRef.Name)
	})

	t.Run("Running -> Running", func(t *testing.T) {
		restore := baseRestore("res-running")
		chr := &chiv1.ClickHouseRestore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      restore.Name,
				Namespace: ns,
			},
			Status: chiv1.ClickHouseRestoreStatus{
				Phase:     chiv1.OperationPhaseRunning,
				StartTime: &started,
			},
		}

		c := newRestoreTestContext(t, instance, chi, backup, restore, chr)
		status, err := p.SyncRestore(c, restore)
		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.RestoreStateRunning, status.State)
		assert.Equal(t, &started, status.StartedAt)
		assert.Nil(t, status.CompletedAt)
		require.NotNil(t, status.OperatorRestoreRef)
	})

	t.Run("Failed -> Failed with condition message", func(t *testing.T) {
		restore := baseRestore("res-failed")
		chr := &chiv1.ClickHouseRestore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      restore.Name,
				Namespace: ns,
			},
			Status: chiv1.ClickHouseRestoreStatus{
				Phase:          chiv1.OperationPhaseFailed,
				StartTime:      &started,
				CompletionTime: &completed,
				Conditions: []metav1.Condition{
					{
						Type:    "Failed",
						Status:  metav1.ConditionFalse,
						Message: "restore job failed: table already exists",
					},
				},
			},
		}

		c := newRestoreTestContext(t, instance, chi, backup, restore, chr)
		status, err := p.SyncRestore(c, restore)
		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.RestoreStateFailed, status.State)
		assert.Equal(t, &started, status.StartedAt)
		assert.Equal(t, &completed, status.CompletedAt)
		assert.Equal(t, "restore job failed: table already exists", status.Message)
		require.NotNil(t, status.OperatorRestoreRef)
	})

	t.Run("Failed -> Failed fallback message", func(t *testing.T) {
		restore := baseRestore("res-failed-fallback")
		chr := &chiv1.ClickHouseRestore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      restore.Name,
				Namespace: ns,
			},
			Status: chiv1.ClickHouseRestoreStatus{
				Phase:          chiv1.OperationPhaseFailed,
				StartTime:      &started,
				CompletionTime: &completed,
			},
		}

		c := newRestoreTestContext(t, instance, chi, backup, restore, chr)
		status, err := p.SyncRestore(c, restore)
		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.RestoreStateFailed, status.State)
		assert.Equal(t, "ClickHouseRestore failed", status.Message)
	})

	t.Run("Pending -> Pending", func(t *testing.T) {
		restore := baseRestore("res-pending")
		chr := &chiv1.ClickHouseRestore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      restore.Name,
				Namespace: ns,
			},
			Status: chiv1.ClickHouseRestoreStatus{
				Phase: chiv1.OperationPhasePending,
			},
		}

		c := newRestoreTestContext(t, instance, chi, backup, restore, chr)
		status, err := p.SyncRestore(c, restore)
		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.RestoreStatePending, status.State)
	})

	t.Run("Empty phase -> Pending", func(t *testing.T) {
		restore := baseRestore("res-empty-phase")
		chr := &chiv1.ClickHouseRestore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      restore.Name,
				Namespace: ns,
			},
			Status: chiv1.ClickHouseRestoreStatus{
				Phase: "",
			},
		}

		c := newRestoreTestContext(t, instance, chi, backup, restore, chr)
		status, err := p.SyncRestore(c, restore)
		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.RestoreStatePending, status.State)
	})
}

func TestSyncRestore_TerminalStateKeptWhenGone(t *testing.T) {
	p := &Provider{}
	ns := "default"
	instName := "test-ch"
	instance := testInstanceWithStorage(instName, ns, "s3-storage")
	chi := testTargetCHI(instName, ns)
	backup := testSuccessfulBackup("term-backup", ns, instName, "s3-storage")

	started := metav1.NewTime(time.Now().Add(-15 * time.Minute).Truncate(time.Second))
	completed := metav1.NewTime(time.Now().Truncate(time.Second))

	t.Run("terminal Succeeded kept when CR is gone", func(t *testing.T) {
		restore := &backupv1alpha1.Restore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "succ-gone-restore",
				Namespace: ns,
			},
			Spec: backupv1alpha1.RestoreSpec{
				InstanceRef: commonv1alpha1.ObjectRef{Name: instName},
				DataSource: backupv1alpha1.DataSource{
					Type: backupv1alpha1.DataSourceTypeBackup,
					Backup: &backupv1alpha1.DataSourceBackup{
						BackupRef: commonv1alpha1.ObjectRef{Name: backup.Name},
					},
				},
			},
			Status: backupv1alpha1.RestoreStatus{
				State:       backupv1alpha1.RestoreStateSucceeded,
				StartedAt:   &started,
				CompletedAt: &completed,
				OperatorRestoreRef: &commonv1alpha1.TypedObjectRef{
					Group: "clickhouse.altinity.com",
					Kind:  "ClickHouseRestore",
					Name:  "succ-gone-restore",
				},
			},
		}

		c := newRestoreTestContext(t, instance, chi, backup, restore) // ClickHouseRestore is gone
		status, err := p.SyncRestore(c, restore)
		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.RestoreStateSucceeded, status.State)
		assert.Equal(t, &started, status.StartedAt)
		assert.Equal(t, &completed, status.CompletedAt)
		require.NotNil(t, status.OperatorRestoreRef)
		assert.Equal(t, "succ-gone-restore", status.OperatorRestoreRef.Name)
	})

	t.Run("terminal Failed kept when CR is gone", func(t *testing.T) {
		restore := &backupv1alpha1.Restore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "failed-gone-restore",
				Namespace: ns,
			},
			Spec: backupv1alpha1.RestoreSpec{
				InstanceRef: commonv1alpha1.ObjectRef{Name: instName},
				DataSource: backupv1alpha1.DataSource{
					Type: backupv1alpha1.DataSourceTypeBackup,
					Backup: &backupv1alpha1.DataSourceBackup{
						BackupRef: commonv1alpha1.ObjectRef{Name: backup.Name},
					},
				},
			},
			Status: backupv1alpha1.RestoreStatus{
				State:   backupv1alpha1.RestoreStateFailed,
				Message: "terminal failure message",
				OperatorRestoreRef: &commonv1alpha1.TypedObjectRef{
					Group: "clickhouse.altinity.com",
					Kind:  "ClickHouseRestore",
					Name:  "failed-gone-restore",
				},
			},
		}

		c := newRestoreTestContext(t, instance, chi, backup, restore) // ClickHouseRestore is gone
		status, err := p.SyncRestore(c, restore)
		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.RestoreStateFailed, status.State)
		assert.Equal(t, "terminal failure message", status.Message)
		require.NotNil(t, status.OperatorRestoreRef)
	})

	t.Run("non-terminal Running fails when CR is gone", func(t *testing.T) {
		restore := &backupv1alpha1.Restore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "running-gone-restore",
				Namespace: ns,
			},
			Spec: backupv1alpha1.RestoreSpec{
				InstanceRef: commonv1alpha1.ObjectRef{Name: instName},
				DataSource: backupv1alpha1.DataSource{
					Type: backupv1alpha1.DataSourceTypeBackup,
					Backup: &backupv1alpha1.DataSourceBackup{
						BackupRef: commonv1alpha1.ObjectRef{Name: backup.Name},
					},
				},
			},
			Status: backupv1alpha1.RestoreStatus{
				State: backupv1alpha1.RestoreStateRunning,
				OperatorRestoreRef: &commonv1alpha1.TypedObjectRef{
					Group: "clickhouse.altinity.com",
					Kind:  "ClickHouseRestore",
					Name:  "running-gone-restore",
				},
			},
		}

		c := newRestoreTestContext(t, instance, chi, backup, restore) // ClickHouseRestore is gone
		status, err := p.SyncRestore(c, restore)
		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.RestoreStateFailed, status.State)
		assert.Equal(t, "ClickHouseRestore is gone", status.Message)
		require.NotNil(t, status.OperatorRestoreRef)
	})
}

func TestSyncRestore_ExistingCHR_BackupDeleted(t *testing.T) {
	p := &Provider{}
	ns := "default"
	instName := "test-ch"
	instance := testInstanceWithStorage(instName, ns, "s3-storage")
	chi := testTargetCHI(instName, ns)

	baseTime := time.Now().Truncate(time.Second)
	startTime := metav1.NewTime(baseTime.Add(-5 * time.Minute))
	completionTime := metav1.NewTime(baseTime.Add(-1 * time.Minute))

	t.Run("CHR exists with phase Running and Backup deleted -> Running", func(t *testing.T) {
		restore := &backupv1alpha1.Restore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "restore-running-backup-deleted",
				Namespace: ns,
			},
			Spec: backupv1alpha1.RestoreSpec{
				InstanceRef: commonv1alpha1.ObjectRef{Name: instName},
				DataSource: backupv1alpha1.DataSource{
					Type: backupv1alpha1.DataSourceTypeBackup,
					Backup: &backupv1alpha1.DataSourceBackup{
						BackupRef: commonv1alpha1.ObjectRef{Name: "deleted-backup"},
					},
				},
			},
		}

		chr := &chiv1.ClickHouseRestore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      restore.Name,
				Namespace: ns,
			},
			Spec: chiv1.ClickHouseRestoreSpec{
				ClickHouseInstallation: instName,
				BackupName:             "deleted-backup",
			},
			Status: chiv1.ClickHouseRestoreStatus{
				Phase:     chiv1.OperationPhaseRunning,
				StartTime: &startTime,
			},
		}

		// Notice: backup is NOT in the context (it was deleted)
		c := newRestoreTestContext(t, instance, chi, restore, chr)

		status, err := p.SyncRestore(c, restore)
		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.RestoreStateRunning, status.State)
		assert.Equal(t, &startTime, status.StartedAt)
		require.NotNil(t, status.OperatorRestoreRef)
		assert.Equal(t, restore.Name, status.OperatorRestoreRef.Name)
	})

	t.Run("CHR exists with phase Completed and Backup deleted -> Succeeded", func(t *testing.T) {
		restore := &backupv1alpha1.Restore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "restore-completed-backup-deleted",
				Namespace: ns,
			},
			Spec: backupv1alpha1.RestoreSpec{
				InstanceRef: commonv1alpha1.ObjectRef{Name: instName},
				DataSource: backupv1alpha1.DataSource{
					Type: backupv1alpha1.DataSourceTypeBackup,
					Backup: &backupv1alpha1.DataSourceBackup{
						BackupRef: commonv1alpha1.ObjectRef{Name: "deleted-backup"},
					},
				},
			},
		}

		chr := &chiv1.ClickHouseRestore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      restore.Name,
				Namespace: ns,
			},
			Spec: chiv1.ClickHouseRestoreSpec{
				ClickHouseInstallation: instName,
				BackupName:             "deleted-backup",
			},
			Status: chiv1.ClickHouseRestoreStatus{
				Phase:          chiv1.OperationPhaseCompleted,
				StartTime:      &startTime,
				CompletionTime: &completionTime,
			},
		}

		// Notice: backup is NOT in the context (it was deleted)
		c := newRestoreTestContext(t, instance, chi, restore, chr)

		status, err := p.SyncRestore(c, restore)
		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.RestoreStateSucceeded, status.State)
		assert.Equal(t, &startTime, status.StartedAt)
		assert.Equal(t, &completionTime, status.CompletedAt)
		require.NotNil(t, status.OperatorRestoreRef)
		assert.Equal(t, restore.Name, status.OperatorRestoreRef.Name)
	})
}

func TestCleanupRestore(t *testing.T) {
	p := &Provider{}
	ns := "default"
	instName := "test-ch"
	instance := testInstanceWithStorage(instName, ns, "s3-storage")

	restore := &backupv1alpha1.Restore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cleanup-restore",
			Namespace: ns,
		},
	}

	chr := &chiv1.ClickHouseRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      restore.Name,
			Namespace: ns,
		},
	}

	c := newRestoreTestContext(t, instance, restore, chr)

	// Exists -> deleted, not done
	done, err := p.CleanupRestore(c, restore)
	require.NoError(t, err)
	assert.False(t, done)

	// Gone -> done
	done, err = p.CleanupRestore(c, restore)
	require.NoError(t, err)
	assert.True(t, done)
}

func TestRestoreWatches(t *testing.T) {
	p := &Provider{}
	watches := p.RestoreWatches()
	require.Len(t, watches, 1)
	assert.Equal(t, &chiv1.ClickHouseRestore{}, watches[0].Object)
	assert.True(t, watches[0].Owned)
}
