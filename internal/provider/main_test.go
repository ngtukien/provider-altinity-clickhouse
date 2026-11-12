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
	"os"
	"testing"

	"github.com/openeverest/provider-altinity-clickhouse/internal/common"
)

// TestMain sets the operator namespace the network policies need, which a
// pod reads from its service account; tests may override it with t.Setenv.
func TestMain(m *testing.M) {
	_ = os.Setenv(common.EnvOperatorNamespace, "clickhouse-provider")
	os.Exit(m.Run())
}
