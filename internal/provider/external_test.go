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
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-altinity-clickhouse/internal/common"
)

func exposedInstance(params string, serviceType corev1.ServiceType) *corev1alpha1.Instance {
	return withEngine(newTestInstance("db", "ns", params), func(e *corev1alpha1.ComponentSpec) {
		e.Service = &corev1alpha1.Service{ServiceType: serviceType}
	})
}

func externalIngressIn(t *testing.T, c *controller.Context, suffix string) *networkingv1.Ingress {
	t.Helper()
	ingress := &networkingv1.Ingress{}
	found, err := getOptional(c, ingress, "db"+suffix)
	require.NoError(t, err)
	if !found {
		return nil
	}
	return ingress
}

func TestExternalAccess(t *testing.T) {
	t.Setenv(common.EnvExternalDomain, "clickhouse.example.com")
	t.Setenv(common.EnvIngressClass, "cilium")
	t.Setenv(common.EnvIngressClusterIssuer, "letsencrypt")

	// Exposed without TLS: HTTPS by name, no native (it needs TLS for SNI).
	c := newTestContext(t, exposedInstance("", corev1.ServiceTypeNodePort))
	require.NoError(t, reconcileExternalAccess(c))
	http := externalIngressIn(t, c, externalHTTPSuffix)
	require.NotNil(t, http)
	assert.Equal(t, "cilium", *http.Spec.IngressClassName)
	assert.Equal(t, "letsencrypt", http.Annotations["cert-manager.io/cluster-issuer"])
	assert.Equal(t, "db.clickhouse.example.com", http.Spec.Rules[0].Host)
	assert.Equal(t, []string{"db.clickhouse.example.com"}, http.Spec.TLS[0].Hosts)
	backend := http.Spec.Rules[0].HTTP.Paths[0].Backend.Service
	assert.Equal(t, "db"+externalServiceSuffix, backend.Name)
	assert.EqualValues(t, 8123, backend.Port.Number)
	assert.Nil(t, externalIngressIn(t, c, externalNativeSuffix))
	svc := &corev1.Service{}
	require.NoError(t, c.Get(svc, "db"+externalServiceSuffix))
	assert.Equal(t, map[string]string{common.LabelCHIName: "db"}, svc.Spec.Selector)

	// Required TLS: native passthrough only, 8123 is closed to the Ingress.
	c = newTestContext(t, exposedInstance(tlsRequiredParams, corev1.ServiceTypeNodePort))
	require.NoError(t, reconcileExternalAccess(c))
	assert.Nil(t, externalIngressIn(t, c, externalHTTPSuffix))
	native := externalIngressIn(t, c, externalNativeSuffix)
	require.NotNil(t, native)
	assert.Equal(t, "enabled", native.Annotations["ingress.cilium.io/tls-passthrough"])
	assert.EqualValues(t, 9440, native.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port.Number)
	names, _, err := externalAddresses(c)
	require.NoError(t, err)
	assert.Contains(t, names, "db.clickhouse.example.com", "the server certificate covers the name")

	// Optional TLS, HTTP only: no native Ingress.
	c = newTestContext(t, exposedInstance(`{"tls":{"enabled":"enabled","mode":"optional"},"network":{"protocols":"http"}}`, corev1.ServiceTypeNodePort))
	require.NoError(t, reconcileExternalAccess(c))
	assert.NotNil(t, externalIngressIn(t, c, externalHTTPSuffix))
	assert.Nil(t, externalIngressIn(t, c, externalNativeSuffix))
}

func TestExternalAccessOnlyWhenExposedWithADomain(t *testing.T) {
	t.Setenv(common.EnvExternalDomain, "clickhouse.example.com")
	c := newTestContext(t, exposedInstance(tlsOptionalParams, corev1.ServiceTypeClusterIP))
	require.NoError(t, reconcileExternalAccess(c))
	assert.Nil(t, externalIngressIn(t, c, externalHTTPSuffix))
	assert.Nil(t, externalIngressIn(t, c, externalNativeSuffix))
	assert.Empty(t, externalHost(c))

	t.Setenv(common.EnvExternalDomain, "")
	c = newTestContext(t, exposedInstance(tlsOptionalParams, corev1.ServiceTypeNodePort))
	assert.Empty(t, externalHost(c))
}

func TestExternalAccessRemovedWhenNoLongerWanted(t *testing.T) {
	t.Setenv(common.EnvExternalDomain, "clickhouse.example.com")
	exposed := newTestContext(t, exposedInstance(tlsOptionalParams, corev1.ServiceTypeNodePort))
	require.NoError(t, reconcileExternalAccess(exposed))
	http, native := externalIngressIn(t, exposed, externalHTTPSuffix), externalIngressIn(t, exposed, externalNativeSuffix)
	require.NotNil(t, http)
	require.NotNil(t, native)

	// Same objects, Instance back to ClusterIP: everything goes.
	http.ResourceVersion, native.ResourceVersion = "", ""
	c := newTestContext(t, exposedInstance(tlsOptionalParams, corev1.ServiceTypeClusterIP), http, native)
	require.NoError(t, reconcileExternalAccess(c))
	assert.Nil(t, externalIngressIn(t, c, externalHTTPSuffix))
	assert.Nil(t, externalIngressIn(t, c, externalNativeSuffix))
}
