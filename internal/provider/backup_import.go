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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
)

// The clickhouse-backup sidecar stores each backup of a shard under
// backup/<namespace>/<installation>/<cluster>/<shard>/<backup>/, with its
// metadata.json there once the upload is complete.
const (
	importPrefix   = "backup/"
	importMetadata = "metadata.json"
)

// storedObject is one object of the storage listing.
type storedObject struct {
	Key          string
	LastModified time.Time
}

// ImportBackups lists the backups in the BackupStorage and returns one
// External Backup per backup, its shards merged, for the runtime to create.
func (p *Provider) ImportBackups(ctx context.Context, imp *backupv1alpha1.BackupImport,
	storage *backupv1alpha1.BackupStorage) (controller.BackupImportExecutionStatus, error) {
	// shortcut: a client per import (ImportBackups gets none), fine while imports are rare.
	cfg, err := config.GetConfig()
	if err != nil {
		return controller.BackupImportExecutionStatus{}, fmt.Errorf("kubernetes config: %w", err)
	}
	kube, err := client.New(cfg, client.Options{})
	if err != nil {
		return controller.BackupImportExecutionStatus{}, fmt.Errorf("kubernetes client: %w", err)
	}
	s3Client, err := controller.NewS3Client(ctx, kube, storage)
	if err != nil {
		return controller.BackupImportExecutionStatus{
			State: backupv1alpha1.BackupImportStateFailed, Message: err.Error(),
		}, nil
	}

	var objects []storedObject
	// Only the import's own namespace: a bucket shared between tenants must not
	// hand one tenant another's backups.
	pages := s3.NewListObjectsV2Paginator(s3Client, &s3.ListObjectsV2Input{
		Bucket: aws.String(storage.Spec.S3.Bucket),
		Prefix: aws.String(importPrefix + imp.Namespace + "/"),
	})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return controller.BackupImportExecutionStatus{}, fmt.Errorf("list bucket %q: %w", storage.Spec.S3.Bucket, err)
		}
		for _, obj := range page.Contents {
			objects = append(objects, storedObject{Key: aws.ToString(obj.Key), LastModified: aws.ToTime(obj.LastModified)})
		}
	}

	backups := importedBackups(imp, objects)
	return controller.BackupImportExecutionStatus{
		Backups: backups,
		State:   backupv1alpha1.BackupImportStateSucceeded,
		Message: fmt.Sprintf("found %d backups in %s", len(backups), storage.Spec.S3.Bucket),
	}, nil
}

// importedBackups turns the metadata.json objects of a storage listing into
// External Backups, one per backup across its shards: the path leaves the
// shard out, and the times are the first and last shard's metadata upload.
// Backups of other namespaces are left out.
func importedBackups(imp *backupv1alpha1.BackupImport, objects []storedObject) []*backupv1alpha1.Backup {
	type span struct{ first, last time.Time }
	spans := map[string]*span{}
	for _, obj := range objects {
		parts := strings.Split(strings.TrimPrefix(obj.Key, importPrefix), "/")
		// namespace, installation, cluster, shard, backup, metadata.json
		if len(parts) != 6 || parts[5] != importMetadata || slicesHasEmpty(parts) || parts[0] != imp.Namespace {
			continue
		}
		path := importPrefix + strings.Join([]string{parts[0], parts[1], parts[2], parts[4]}, "/")
		s := spans[path]
		if s == nil {
			s = &span{first: obj.LastModified, last: obj.LastModified}
			spans[path] = s
		}
		if obj.LastModified.Before(s.first) {
			s.first = obj.LastModified
		}
		if obj.LastModified.After(s.last) {
			s.last = obj.LastModified
		}
	}

	paths := make([]string, 0, len(spans))
	for path := range spans {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	backups := make([]*backupv1alpha1.Backup, 0, len(paths))
	for _, path := range paths {
		s := spans[path]
		backups = append(backups, &backupv1alpha1.Backup{
			ObjectMeta: metav1.ObjectMeta{Name: importedBackupName(path)},
			Spec: backupv1alpha1.BackupSpec{
				Origin: backupv1alpha1.BackupOrigin{
					Type: backupv1alpha1.BackupOriginTypeExternal,
					External: &backupv1alpha1.BackupOriginExternal{
						Path:        path,
						StartedAt:   metav1.NewTime(s.first),
						CompletedAt: metav1.NewTime(s.last),
					},
				},
				ClassRef:   imp.Spec.ClassRef,
				StorageRef: imp.Spec.StorageRef,
				// External Backups must not delete the data they point at.
				DeletionPolicy: backupv1alpha1.BackupDeletionPolicyRetain,
			},
		})
	}
	return backups
}

func slicesHasEmpty(parts []string) bool {
	for _, p := range parts {
		if p == "" {
			return true
		}
	}
	return false
}

var invalidNameChars = regexp.MustCompile(`[^a-z0-9-]+`)

// importedBackupName names the Backup after the backup and a hash of its path,
// so the same backup always gets the same name and two installations' backups
// of the same name do not collide.
func importedBackupName(path string) string {
	sum := sha256.Sum256([]byte(path))
	name := invalidNameChars.ReplaceAllString(strings.ToLower(path[strings.LastIndex(path, "/")+1:]), "-")
	name = strings.Trim(name, "-")
	if len(name) > 50 {
		name = strings.TrimRight(name[:50], "-")
	}
	if name == "" {
		name = "backup"
	}
	return name + "-" + hex.EncodeToString(sum[:])[:8]
}

// importedSource reads the namespace, installation and backup name from the
// path of an imported Backup (backup/<namespace>/<installation>/<cluster>/<backup>).
func importedSource(path string) (namespace, installation, name string, ok bool) {
	parts := strings.Split(strings.TrimPrefix(path, importPrefix), "/")
	if !strings.HasPrefix(path, importPrefix) || len(parts) != 4 || slicesHasEmpty(parts) {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[3], true
}
