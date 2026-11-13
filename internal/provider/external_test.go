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

	chiv1 "github.com/altinity/clickhouse-operator/pkg/apis/clickhouse.altinity.com/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

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
}

func TestExternalAccessOnlyWhenExposedWithADomain(t *testing.T) {
	t.Setenv(common.EnvExternalDomain, "clickhouse.example.com")
	c := newTestContext(t, exposedInstance(tlsOptionalParams, corev1.ServiceTypeClusterIP))
	require.NoError(t, reconcileExternalAccess(c))
	assert.Nil(t, externalIngressIn(t, c, externalHTTPSuffix))
	assert.Nil(t, externalIngressIn(t, c, externalNativeSuffix))
	assert.Empty(t, externalHost(c))

	// A LoadBalancer has its own address, limited by its source ranges.
	c = newTestContext(t, exposedInstance(tlsOptionalParams, corev1.ServiceTypeLoadBalancer))
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

func TestConnectionDetailsUseExternalHost(t *testing.T) {
	t.Setenv(common.EnvExternalDomain, "clickhouse.example.com")
	chi := completedCHI()

	// HTTPS through the Ingress on 443.
	details := buildConnectionDetails(newTestContext(t, exposedInstance(tlsOptionalParams, corev1.ServiceTypeNodePort), issuedCertificate()), chi)
	assert.Equal(t, "db.clickhouse.example.com", details.Host)
	assert.Equal(t, "443", details.Port)
	assert.Regexp(t, `^https://[^@]+@db\.clickhouse\.example\.com:443/$`, details.URI)
	assert.Equal(t, "db.clickhouse.example.com", details.AdditionalProperties[common.ConnectionKeyExternalHost])
	assert.NotEqual(t, details.Host, details.AdditionalProperties[common.ConnectionKeyInternalHost])

	// Required TLS closes HTTP to the Ingress: native TLS on 9440.
	details = buildConnectionDetails(newTestContext(t, exposedInstance(tlsRequiredParams, corev1.ServiceTypeNodePort), issuedCertificate()), chi)
	assert.Equal(t, "9440", details.Port)
	assert.Regexp(t, `^clickhouse://[^@]+@db\.clickhouse\.example\.com:9440/\?secure=true$`, details.URI)

	// In-cluster only: unchanged.
	details = buildConnectionDetails(newTestContext(t, exposedInstance(tlsOptionalParams, corev1.ServiceTypeClusterIP), issuedCertificate()), chi)
	assert.NotContains(t, details.Host, "example.com")
	assert.Equal(t, "8443", details.Port)
}

func TestLoadBalancerExposure(t *testing.T) {
	enginePath := field.NewPath("spec", "components").Key("engine")

	// Whether customers may pick a LoadBalancer is the form's business.
	assert.Empty(t, validateExposure(enginePath, "", ""))
	assert.Empty(t, validateExposure(enginePath, "203.0.113.0/24, 198.51.100.7/32", common.ExternalPortsTLS))
	assert.Len(t, validateExposure(enginePath, "203.0.113.1/24,nope", ""), 2)
	errs := validateExposure(enginePath, "", "plaintext")
	require.Len(t, errs, 1)
	assert.Equal(t, "spec.components[engine].parameters.externalPorts", errs[0].Field)

	// The parameter becomes the Service's source ranges, unless the Service sets its own.
	params := `{"loadBalancerSourceRanges":"203.0.113.0/24, 198.51.100.7/32"}`
	c := newTestContext(t, exposedInstance(params, corev1.ServiceTypeLoadBalancer))
	assert.Equal(t, corev1alpha1.SourceRanges{"203.0.113.0/24", "198.51.100.7/32"}, exposedService(c).LoadBalancerService.SourceRanges)
	spec := &chiv1.ChiSpec{}
	configureExpose(spec, exposedService(c), "")
	assert.Equal(t, []string{"203.0.113.0/24", "198.51.100.7/32"}, spec.Templates.ServiceTemplates[0].Spec.LoadBalancerSourceRanges)

	own := withEngine(exposedInstance(params, corev1.ServiceTypeLoadBalancer), func(e *corev1alpha1.ComponentSpec) {
		e.Service.LoadBalancerService = &corev1alpha1.LoadBalancerService{SourceRanges: corev1alpha1.SourceRanges{"192.0.2.0/24"}}
	})
	assert.Equal(t, corev1alpha1.SourceRanges{"192.0.2.0/24"}, exposedService(newTestContext(t, own)).LoadBalancerService.SourceRanges)

	// Ignored by other exposures.
	c = newTestContext(t, exposedInstance(params, corev1.ServiceTypeNodePort))
	assert.Nil(t, exposedService(c).LoadBalancerService)
}

// With externalPorts "tls" the exposure moves to <name>-public with the secure
// ports only; the client Service stays in the cluster with every port.
func TestExternalPortsTLS(t *testing.T) {
	params := `{"tls":{"enabled":"enabled","mode":"optional"},"externalPorts":"tls","loadBalancerSourceRanges":"203.0.113.0/24"}`
	for _, serviceType := range []corev1.ServiceType{corev1.ServiceTypeNodePort, corev1.ServiceTypeLoadBalancer} {
		t.Run(string(serviceType), func(t *testing.T) {
			c := newTestContext(t, exposedInstance(params, serviceType), issuedCertificate())
			require.True(t, externalTLSOnly(c))

			chi, err := buildCHI(c, 2)
			require.NoError(t, err)
			tmpl := chi.Spec.Templates.ServiceTemplates[0].Spec
			assert.Equal(t, corev1.ServiceTypeClusterIP, tmpl.Type, "the client Service stays in the cluster")
			assert.Len(t, tmpl.Ports, 4, "plaintext and secure ports in the cluster")

			require.NoError(t, reconcileExternalAccess(c))
			public := &corev1.Service{}
			require.NoError(t, c.Get(public, "db"+publicServiceSuffix))
			assert.Equal(t, serviceType, public.Spec.Type)
			var ports []int32
			for _, p := range public.Spec.Ports {
				ports = append(ports, p.Port)
			}
			assert.Equal(t, []int32{common.HTTPSPort, common.TCPSecurePort}, ports, "secure ports only outside")
			if serviceType == corev1.ServiceTypeLoadBalancer {
				assert.Equal(t, []string{"203.0.113.0/24"}, public.Spec.LoadBalancerSourceRanges)
			}
		})
	}

	// Connection details read the node ports from <name>-public.
	public := publicService(newTestContext(t, exposedInstance(params, corev1.ServiceTypeNodePort)))
	public.Spec.Ports[0].NodePort, public.Spec.Ports[1].NodePort = 31443, 32440
	c := newTestContext(t, exposedInstance(params, corev1.ServiceTypeNodePort), issuedCertificate(), public)
	details := buildConnectionDetails(c, completedCHI())
	assert.Equal(t, "31443", details.AdditionalProperties[common.ConnectionKeyHTTPSPort+common.ConnectionKeyNodePortSuffix])
	assert.Equal(t, "32440", details.AdditionalProperties[common.ConnectionKeyNativeSecurePort+common.ConnectionKeyNodePortSuffix])
	assert.NotContains(t, details.AdditionalProperties, common.ConnectionKeyHTTPPort+common.ConnectionKeyNodePortSuffix)

	// Default "all": as before, the client Service itself is exposed and no -public Service.
	c = newTestContext(t, exposedInstance(`{"tls":{"enabled":"enabled","mode":"optional"}}`, corev1.ServiceTypeNodePort), issuedCertificate())
	assert.False(t, externalTLSOnly(c))
	require.NoError(t, reconcileExternalAccess(c))
	found, err := getOptional(c, &corev1.Service{}, "db"+publicServiceSuffix)
	require.NoError(t, err)
	assert.False(t, found)
}
