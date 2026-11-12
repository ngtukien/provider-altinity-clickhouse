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

// Package components contains custom spec types for provider component types.
//
// Each struct here corresponds to a component type defined in versions.yaml
// and is converted to an OpenAPI schema during generation.
// Add fields when a component type needs custom configuration beyond
// what the base Instance spec provides.
//
// +k8s:openapi-gen=true
package components

// ClickHouseParameters defines custom parameters for the ClickHouse engine component.
// Provider users specify these fields in the Instance's engine component parameters.
type ClickHouseParameters struct {
	// Configuration holds custom ClickHouse server settings as YAML.
	// Each entry maps to a server-level setting rendered into config.d,
	// e.g. `max_concurrent_queries: 200` or `logger/level: information`.
	Configuration string `json:"configuration,omitempty"`

	// PodMonitor gates creation of a Prometheus PodMonitor for the ClickHouse
	// pods. The native metrics endpoint is always exposed; this only controls
	// whether the operator manages a PodMonitor. One of "enabled" or "disabled".
	PodMonitor string `json:"podMonitor,omitempty"`

	// TLS configures encrypted connections. When enabled, the provider uses
	// cert-manager to issue a server certificate, serves HTTPS (8443) and the
	// secure native protocol (9440), and runs distributed queries between
	// replicas over TLS. See TLSParameters.Mode for the plaintext ports.
	TLS *TLSParameters `json:"tls,omitempty"`

	// StorageReclaimPolicy controls what happens to the data volumes (ClickHouse
	// and, in the replicated topology, Keeper) when the Instance is deleted or a
	// replica is removed. One of "Retain" (default) or "Delete". Retained volumes
	// are re-attached when an Instance with the same name is created in the same
	// namespace.
	StorageReclaimPolicy string `json:"storageReclaimPolicy,omitempty"`
	// QueryLimits caps the memory and time queries may use, so a heavy query
	// fails with MEMORY_LIMIT_EXCEEDED or TIMEOUT_EXCEEDED instead of the pod
	// being OOM-killed. Applied with defaults when omitted.
	QueryLimits *QueryLimitsParameters `json:"queryLimits,omitempty"`
	// ColdStorage adds an S3 disk and a storage policy "tiered" (volumes "hot"
	// on the data volume, "cold" on S3) for tables that opt in with
	// `SETTINGS storage_policy = 'tiered'` and move old data with
	// `TTL ... TO VOLUME 'cold'`. The default policy is unchanged.
	ColdStorage *ColdStorageParameters `json:"coldStorage,omitempty"`
}

// QueryLimitsParameters configures the server memory budget and the settings
// profiles: "default" (the application user and SQL-created users without a
// profile) and "readonly" (for BI users: `CREATE USER bi ... SETTINGS PROFILE 'readonly'`).
type QueryLimitsParameters struct {
	// ServerMemoryPercent is the share of the engine's memory limit all queries
	// together may use (max_server_memory_usage), from 10 to 95. Default 80;
	// the rest is left to memory ClickHouse does not track.
	ServerMemoryPercent int `json:"serverMemoryPercent,omitempty"`
	// MaxMemoryUsage caps the memory of one query (max_memory_usage), as a
	// Kubernetes quantity such as "2Gi". Default half the engine's memory
	// limit; must not exceed the server memory budget.
	MaxMemoryUsage string `json:"maxMemoryUsage,omitempty"`
	// MaxExecutionTime cancels queries of the default profile running longer
	// than this many seconds (max_execution_time). Default 0, no limit, so
	// long ETL and INSERT ... SELECT keep working.
	MaxExecutionTime int `json:"maxExecutionTime,omitempty"`
	// ReadonlyMaxExecutionTime is max_execution_time of the readonly profile,
	// in seconds. Default 300.
	ReadonlyMaxExecutionTime int `json:"readonlyMaxExecutionTime,omitempty"`
	// SpillToDisk lets GROUP BY and ORDER BY write to disk once they use half
	// of MaxMemoryUsage instead of failing. One of "enabled" (default) or "disabled".
	SpillToDisk string `json:"spillToDisk,omitempty"`
}

// ColdStorageParameters configures the S3 tier of the "tiered" storage policy.
type ColdStorageParameters struct {
	// StorageRef names a BackupStorage (backup.openeverest.io) in the
	// Instance's namespace whose S3 bucket, endpoint and credentials hold the
	// cold data, under <namespace>/<instance>/cold/.
	StorageRef string `json:"storageRef"`
	// CachePercent is the share of the engine's storage size used as a local
	// read cache for cold data, from 1 to 50. Default 10.
	CachePercent int `json:"cachePercent,omitempty"`
}

// TLSParameters configures TLS for client connections to ClickHouse.
type TLSParameters struct {
	// Enabled turns on TLS. One of "enabled" or "disabled" (default). Requires
	// cert-manager to be installed in the cluster.
	Enabled string `json:"enabled,omitempty"`

	// Mode is "required" (default) or "optional". Required exposes only the
	// secure ports to clients and restricts the plaintext ports to the
	// operator and the Instance's own pods with a NetworkPolicy. Optional
	// keeps the plaintext ports open alongside the secure ones.
	Mode string `json:"mode,omitempty"`

	// IssuerRef optionally references an existing cert-manager Issuer or
	// ClusterIssuer to sign the server certificate. When omitted, the provider
	// creates a self-signed CA chain scoped to this Instance.
	IssuerRef *IssuerRef `json:"issuerRef,omitempty"`
}

// IssuerRef references a cert-manager issuer used to sign the server certificate.
type IssuerRef struct {
	// Name is the name of the Issuer or ClusterIssuer.
	Name string `json:"name"`

	// Kind is the issuer kind, either "Issuer" or "ClusterIssuer". Defaults to "Issuer".
	Kind string `json:"kind,omitempty"`

	// Group is the API group of the issuer. Defaults to "cert-manager.io".
	Group string `json:"group,omitempty"`
}
