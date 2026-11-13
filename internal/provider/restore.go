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
	"encoding/json"
	"fmt"

	chiv1 "github.com/altinity/clickhouse-operator/pkg/apis/clickhouse.altinity.com/v1"
	"github.com/altinity/clickhouse-operator/pkg/apis/common/types"
	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Compile-time interface checks.
var _ controller.RestoreWatcher = (*Provider)(nil)

type restoreParameters struct {
	Overwrite  bool   `json:"overwrite"`
	SchemaOnly bool   `json:"schemaOnly"`
	Tables     string `json:"tables"`
}

func decodeRestoreParameters(raw *runtime.RawExtension) (restoreParameters, error) {
	if raw == nil || (len(raw.Raw) == 0 && raw.Object == nil) {
		return restoreParameters{}, nil
	}
	var params restoreParameters
	if len(raw.Raw) > 0 {
		if err := json.Unmarshal(raw.Raw, &params); err != nil {
			return restoreParameters{}, fmt.Errorf("invalid restore parameters: %w", err)
		}
	} else if raw.Object != nil {
		data, err := json.Marshal(raw.Object)
		if err != nil {
			return restoreParameters{}, fmt.Errorf("marshal restore parameters: %w", err)
		}
		if err := json.Unmarshal(data, &params); err != nil {
			return restoreParameters{}, fmt.Errorf("invalid restore parameters: %w", err)
		}
	}
	return params, nil
}

func restoreFailureMessage(status *chiv1.ClickHouseRestoreStatus) string {
	for _, c := range status.Conditions {
		if (c.Status == metav1.ConditionFalse || c.Type == "Failed" || c.Reason == "Failed") && c.Message != "" {
			return c.Message
		}
	}
	for _, cl := range status.Clusters {
		if cl.Reason != "" {
			return cl.Reason
		}
		if cl.Message != "" {
			return cl.Message
		}
	}
	for _, c := range status.Conditions {
		if (c.Status == metav1.ConditionFalse || c.Type == "Failed" || c.Reason == "Failed") && c.Reason != "" {
			return c.Reason
		}
	}
	return "ClickHouseRestore failed"
}

// SyncRestore creates or updates the operator's ClickHouseRestore resource, sets a
// controller reference from the Restore CR to enable owner-based watches, and maps
// operator status to OpenEverest states.
func (p *Provider) SyncRestore(c *controller.Context, restore *backupv1alpha1.Restore) (controller.RestoreExecutionStatus, error) {
	l := log.FromContext(c.Context())
	l.Info("Syncing restore", "name", restore.Name)

	if restore.Namespace == "" {
		restore.Namespace = c.Namespace()
	}

	chr := &chiv1.ClickHouseRestore{}
	err := c.Get(chr, restore.Name)
	if err != nil && !apierrors.IsNotFound(err) {
		return controller.RestoreExecutionStatus{}, fmt.Errorf("get ClickHouseRestore: %w", err)
	}

	exists := err == nil

	if !exists {
		if restore.Status.OperatorRestoreRef != nil {
			if restore.Status.State == backupv1alpha1.RestoreStateSucceeded || restore.Status.State == backupv1alpha1.RestoreStateFailed {
				return controller.RestoreExecutionStatus{
					State:              restore.Status.State,
					Message:            restore.Status.Message,
					StartedAt:          restore.Status.StartedAt,
					CompletedAt:        restore.Status.CompletedAt,
					OperatorRestoreRef: restore.Status.OperatorRestoreRef,
				}, nil
			}
			return controller.RestoreExecutionStatus{
				State:              backupv1alpha1.RestoreStateFailed,
				Message:            "ClickHouseRestore is gone",
				OperatorRestoreRef: restore.Status.OperatorRestoreRef,
			}, nil
		}

		if restore.Spec.DataSource.Type == backupv1alpha1.DataSourceTypePointInTime {
			return controller.RestoreExecutionStatus{
				State:   backupv1alpha1.RestoreStateFailed,
				Message: "point-in-time restore is not supported",
			}, nil
		}

		if restore.Spec.DataSource.Backup == nil || restore.Spec.DataSource.Backup.BackupRef.Name == "" {
			return controller.RestoreExecutionStatus{
				State:   backupv1alpha1.RestoreStateFailed,
				Message: "backup data source is required",
			}, nil
		}

		backupName := restore.Spec.DataSource.Backup.BackupRef.Name
		backup := &backupv1alpha1.Backup{}
		if err := c.Get(backup, backupName); err != nil {
			if apierrors.IsNotFound(err) {
				return controller.RestoreExecutionStatus{
					State:   backupv1alpha1.RestoreStateFailed,
					Message: fmt.Sprintf("Backup %q not found", backupName),
				}, nil
			}
			return controller.RestoreExecutionStatus{}, fmt.Errorf("get Backup: %w", err)
		}

		// An imported Backup names its source in its path (see ImportBackups).
		sourceNamespace, sourceInstallation, sourceBackup := "", "", remoteBackupName(backup)
		if backup.Spec.Origin.InstanceRef != nil && backup.Spec.Origin.InstanceRef.Name != c.Name() {
			sourceInstallation = backup.Spec.Origin.InstanceRef.Name
		}
		if external := backup.Spec.Origin.External; external != nil {
			ns, installation, name, ok := importedSource(external.Path)
			if !ok {
				return controller.RestoreExecutionStatus{
					State:   backupv1alpha1.RestoreStateFailed,
					Message: fmt.Sprintf("Backup %q has an unknown path %q", backupName, external.Path),
				}, nil
			}
			sourceBackup = name
			if installation != c.Name() || ns != restore.Namespace {
				sourceInstallation = installation
			}
			if ns != restore.Namespace {
				sourceNamespace = ns
			}
		}

		if backup.Status.State == backupv1alpha1.BackupStateFailed {
			return controller.RestoreExecutionStatus{
				State:   backupv1alpha1.RestoreStateFailed,
				Message: fmt.Sprintf("source Backup %q has failed", backupName),
			}, nil
		}
		if backup.Status.State != backupv1alpha1.BackupStateSucceeded {
			return controller.RestoreExecutionStatus{
				State:   backupv1alpha1.RestoreStatePending,
				Message: fmt.Sprintf("waiting for Backup %q to succeed", backupName),
			}, nil
		}

		if !instanceHasBackupStorage(c.Instance(), backup.Spec.StorageRef.Name) {
			return controller.RestoreExecutionStatus{
				State:   backupv1alpha1.RestoreStateFailed,
				Message: fmt.Sprintf("storage %q is not registered and enabled on Instance %q", backup.Spec.StorageRef.Name, c.Name()),
			}, nil
		}

		chi := &chiv1.ClickHouseInstallation{}
		if err := c.Get(chi, c.Name()); err != nil {
			if apierrors.IsNotFound(err) {
				return controller.RestoreExecutionStatus{
					State:   backupv1alpha1.RestoreStatePending,
					Message: "Waiting for ClickHouseInstallation",
				}, nil
			}
			return controller.RestoreExecutionStatus{}, fmt.Errorf("get ClickHouseInstallation: %w", err)
		}

		params, err := decodeRestoreParameters(restore.Spec.Parameters)
		if err != nil {
			return controller.RestoreExecutionStatus{
				State:   backupv1alpha1.RestoreStateFailed,
				Message: err.Error(),
			}, nil
		}

		chr = &chiv1.ClickHouseRestore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      restore.Name,
				Namespace: restore.Namespace,
			},
		}

		if _, err := controllerutil.CreateOrUpdate(c.Context(), c.Client(), chr, func() error {
			if chr.CreationTimestamp.IsZero() {
				spec := chiv1.ClickHouseRestoreSpec{
					ClickHouseInstallation: c.Name(),
					BackupName:             sourceBackup,
					SourceInstallation:     sourceInstallation,
					SourceNamespace:        sourceNamespace,
				}
				if params.Overwrite {
					spec.Overwrite = types.NewStringBool(true)
				}
				if params.SchemaOnly {
					spec.SchemaOnly = types.NewStringBool(true)
				}
				if params.Tables != "" {
					spec.Tables = params.Tables
				}
				chr.Spec = spec
			}
			return controllerutil.SetControllerReference(restore, chr, c.Client().Scheme())
		}); err != nil {
			return controller.RestoreExecutionStatus{}, fmt.Errorf("create or update ClickHouseRestore: %w", err)
		}
	}

	exec := controller.RestoreExecutionStatus{
		OperatorRestoreRef: &commonv1alpha1.TypedObjectRef{
			Group: "clickhouse.altinity.com",
			Kind:  "ClickHouseRestore",
			Name:  chr.Name,
		},
		State: backupv1alpha1.RestoreStatePending,
	}

	switch chr.Status.Phase {
	case chiv1.OperationPhaseCompleted:
		exec.State = backupv1alpha1.RestoreStateSucceeded
		exec.StartedAt = chr.Status.StartTime
		exec.CompletedAt = chr.Status.CompletionTime
	case chiv1.OperationPhaseRunning:
		exec.State = backupv1alpha1.RestoreStateRunning
		exec.StartedAt = chr.Status.StartTime
	case chiv1.OperationPhaseFailed:
		exec.State = backupv1alpha1.RestoreStateFailed
		exec.StartedAt = chr.Status.StartTime
		exec.CompletedAt = chr.Status.CompletionTime
		exec.Message = restoreFailureMessage(&chr.Status)
	case chiv1.OperationPhasePending:
		exec.State = backupv1alpha1.RestoreStatePending
	default:
		exec.State = backupv1alpha1.RestoreStatePending
	}

	return exec, nil
}

// CleanupRestore removes restore resources.
func (p *Provider) CleanupRestore(c *controller.Context, restore *backupv1alpha1.Restore) (bool, error) {
	l := log.FromContext(c.Context())
	l.Info("Cleaning up restore", "name", restore.Name)

	if restore.Namespace == "" {
		restore.Namespace = c.Namespace()
	}

	chr := &chiv1.ClickHouseRestore{}
	if err := c.Get(chr, restore.Name); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, fmt.Errorf("get ClickHouseRestore: %w", err)
	}

	if chr.DeletionTimestamp.IsZero() {
		if err := c.Delete(chr); err != nil && !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("delete ClickHouseRestore: %w", err)
		}
	}
	return false, nil
}

// RestoreWatches implements controller.RestoreWatcher.
func (p *Provider) RestoreWatches() []controller.WatchConfig {
	return []controller.WatchConfig{
		controller.WatchOwned(&chiv1.ClickHouseRestore{}),
	}
}
