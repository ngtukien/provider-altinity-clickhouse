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

// Package altinityclickhousebackup contains schema-bearing Go types for the
// "altinity-clickhouse-backup" BackupClass.
//
// +k8s:openapi-gen=true
package altinityclickhousebackup

// AltinityClickhouseBackupConfig is validated against Backup.spec.parameters and
// per-schedule parameters.
//
// +k8s:openapi-gen=true
type AltinityClickhouseBackupConfig struct {
	// ReplicaSelection picks which replicas are backed up. FirstPerShard (default) backs up
	// one replica of each shard, which is complete only for Replicated* tables. AllReplicas
	// is needed when the cluster holds non-replicated tables.
	// +kubebuilder:validation:Enum=FirstPerShard;AllReplicas
	ReplicaSelection string `json:"replicaSelection,omitempty"`
}

// AltinityClickhouseRestoreConfig is validated against Restore.spec.parameters.
//
// +k8s:openapi-gen=true
type AltinityClickhouseRestoreConfig struct {
	// Overwrite allows restoring over existing tables, which are dropped first.
	// When false (default), the restore is refused if any target host already holds user tables.
	Overwrite bool `json:"overwrite,omitempty"`
	// SchemaOnly restores table and database definitions only, skipping table data.
	SchemaOnly bool `json:"schemaOnly,omitempty"`
	// Tables optionally restricts the restore to comma-separated table names or patterns
	// (e.g. "db.table", "db.*") matching the clickhouse-backup syntax. Empty restores everything.
	Tables string `json:"tables,omitempty"`
}

