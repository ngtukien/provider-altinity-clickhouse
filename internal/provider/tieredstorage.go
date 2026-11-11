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
	"net/url"
	"strconv"
	"strings"

	chiv1 "github.com/altinity/clickhouse-operator/pkg/apis/clickhouse.altinity.com/v1"
	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/openeverest/provider-altinity-clickhouse/definition/components"
	"github.com/openeverest/provider-altinity-clickhouse/internal/common"
)

const (
	coldDisk            = "s3_cold"
	coldCacheDisk       = "s3_cold_cache"
	coldCachePath       = "/var/lib/clickhouse/" + coldCacheDisk + "/"
	tieredPolicy        = "tiered"
	defaultCachePercent = 10
	// objectDiskPath is where clickhouse-backup copies the parts of S3 disks,
	// inside the backup bucket.
	objectDiskPath = "object_disks"
)

// coldStorageParams returns the engine's coldStorage parameters, nil when unset.
func coldStorageParams(c *controller.Context) *components.ColdStorageParameters {
	var params components.ClickHouseParameters
	_ = c.TryDecodeComponentParameters(c.Instance().Spec.Components[common.ComponentEngine], &params)
	return params.ColdStorage
}

// resolveColdStorage reads the BackupStorage behind coldStorage. It returns a
// WaitError while the BackupStorage is missing, so sync must call it directly
// (the runtime does not unwrap wait errors).
func resolveColdStorage(c *controller.Context) (*backupv1alpha1.BackupStorage, error) {
	p := coldStorageParams(c)
	if p == nil {
		return nil, nil
	}
	bs, err := c.BackupStorage(p.StorageRef)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, controller.WaitFor(fmt.Sprintf("BackupStorage %q for cold storage not yet present", p.StorageRef))
		}
		return nil, fmt.Errorf("resolve cold storage: %w", err)
	}
	if bs.Spec.S3 == nil {
		return nil, fmt.Errorf("cold storage: BackupStorage %q is not S3", bs.Name)
	}
	if bs.Spec.S3.CredentialsSecretRef.Name == "" {
		return nil, fmt.Errorf("cold storage: BackupStorage %q has no credentials Secret", bs.Name)
	}
	return bs, nil
}

// coldEndpoint is the S3 disk endpoint: the bucket in the path with
// forcePathStyle, else as a subdomain of the endpoint host.
func coldEndpoint(s3 *backupv1alpha1.BackupStorageS3Spec, namespace, instance string) (string, error) {
	u, err := url.Parse(strings.TrimSuffix(s3.EndpointURL, "/"))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("cold storage: invalid endpointURL %q", s3.EndpointURL)
	}
	prefix := fmt.Sprintf("%s/%s/cold/", namespace, instance)
	if s3.ForcePathStyle != nil && *s3.ForcePathStyle {
		u.Path += "/" + s3.Bucket + "/" + prefix
	} else {
		u.Host = s3.Bucket + "." + u.Host
		u.Path += "/" + prefix
	}
	return u.String(), nil
}

// applyColdStorage adds the S3 disk, its read cache and the tiered policy to
// the server settings. Credentials come from the BackupStorage's Secret as
// environment variables, never inlined.
// shortcut: verifyTLS=false is not honoured (ClickHouse has no per-disk switch); use a trusted endpoint.
func applyColdStorage(settings *chiv1.Settings, bs *backupv1alpha1.BackupStorage, p *components.ColdStorageParameters,
	namespace, instance string, storageSize resource.Quantity) error {
	endpoint, err := coldEndpoint(bs.Spec.S3, namespace, instance)
	if err != nil {
		return err
	}
	percent := p.CachePercent
	if percent == 0 {
		percent = defaultCachePercent
	}
	disk := "storage_configuration/disks/" + coldDisk + "/"
	secret := bs.Spec.S3.CredentialsSecretRef.Name
	settings.Set(disk+"type", chiv1.NewSettingScalar("s3"))
	settings.Set(disk+"endpoint", chiv1.NewSettingScalar(endpoint))
	settings.Set(disk+"access_key_id", secretSetting(secret, "AWS_ACCESS_KEY_ID"))
	settings.Set(disk+"secret_access_key", secretSetting(secret, "AWS_SECRET_ACCESS_KEY"))
	if bs.Spec.S3.Region != "" {
		settings.Set(disk+"region", chiv1.NewSettingScalar(bs.Spec.S3.Region))
	}

	cache := "storage_configuration/disks/" + coldCacheDisk + "/"
	settings.Set(cache+"type", chiv1.NewSettingScalar("cache"))
	settings.Set(cache+"disk", chiv1.NewSettingScalar(coldDisk))
	// Not under disks/: the image entrypoint creates the cache path as root and
	// chowns only the leaf, leaving disks/ root-owned so ClickHouse cannot create
	// the S3 disk's metadata directory there and crashes on start.
	settings.Set(cache+"path", chiv1.NewSettingScalar(coldCachePath))
	settings.Set(cache+"max_size", chiv1.NewSettingScalar(strconv.FormatInt(storageSize.Value()*int64(percent)/100, 10)))

	policy := "storage_configuration/policies/" + tieredPolicy + "/volumes/"
	settings.Set(policy+"hot/disk", chiv1.NewSettingScalar("default"))
	settings.Set(policy+"cold/disk", chiv1.NewSettingScalar(coldCacheDisk))
	// New parts go to the first volume. The operator renders keys sorted, which
	// puts cold before hot, so order the volumes explicitly.
	settings.Set(policy+"hot/volume_priority", chiv1.NewSettingScalar("1"))
	settings.Set(policy+"cold/volume_priority", chiv1.NewSettingScalar("2"))
	return nil
}

// validateColdStorage checks the coldStorage parameters; whether the
// BackupStorage exists is checked when reconciling.
func validateColdStorage(path *field.Path, p *components.ColdStorageParameters) field.ErrorList {
	if p == nil {
		return nil
	}
	var errs field.ErrorList
	if strings.TrimSpace(p.StorageRef) == "" {
		errs = append(errs, field.Required(path.Child("storageRef"), "name a BackupStorage holding the cold data"))
	}
	if p.CachePercent != 0 && (p.CachePercent < 1 || p.CachePercent > 50) {
		errs = append(errs, field.Invalid(path.Child("cachePercent"), p.CachePercent, "must be between 1 and 50"))
	}
	return errs
}
