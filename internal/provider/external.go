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
	"os"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-altinity-clickhouse/internal/common"
)

// External access by name: with CLICKHOUSE_EXTERNAL_DOMAIN set, an Instance
// exposed outside the cluster (NodePort or LoadBalancer) is also reachable as
// <name>.<domain> through the cluster's Ingress controller, on standard ports:
//
//   - HTTP: https://<name>.<domain> (443), TLS ended at the Ingress with a
//     certificate from CLICKHOUSE_INGRESS_CLUSTER_ISSUER, then 8123. Not with
//     required TLS, which closes 8123 to everyone but the Instance's pods.
//   - native: <name>.<domain>:9440, TLS passed through to ClickHouse and routed
//     by SNI (the Ingress controller's TLS passthrough listener). Needs TLS on;
//     the server certificate covers the name.
//
// Plain native (9000) carries no host name, so it cannot be routed by name.
// The wildcard DNS record *.<domain> pointing at the Ingress controller is the
// platform's job.

const (
	// externalHTTPSPort is where the Ingress controller serves HTTPS.
	externalHTTPSPort     = 443
	externalServiceSuffix = "-external"
	externalHTTPSuffix    = "-http"
	externalNativeSuffix  = "-native"
	externalTLSSuffix     = "-external-tls"
)

// externalHost returns <name>.<domain>, or "" when the provider has no domain
// or the Instance is not exposed outside the cluster.
func externalHost(c *controller.Context) string {
	domain := os.Getenv(common.EnvExternalDomain)
	service := c.Instance().Spec.Components[common.ComponentEngine].Service
	if domain == "" || service == nil ||
		(service.ServiceType != corev1.ServiceTypeNodePort && service.ServiceType != corev1.ServiceTypeLoadBalancer) {
		return ""
	}
	return c.Name() + "." + domain
}

// reconcileExternalAccess applies the Service and Ingresses that make the
// Instance reachable by name, and removes those no longer wanted.
func reconcileExternalAccess(c *controller.Context) error {
	host := externalHost(c)
	mode := tlsMode(c)
	wantHTTP := host != "" && mode != common.TLSModeRequired
	wantNative := host != "" && mode != ""

	if err := reconcileObject(c, &networkingv1.Ingress{ObjectMeta: c.ObjectMeta(c.Name() + externalHTTPSuffix)}, wantHTTP,
		func() client.Object { return externalHTTPIngress(c, host) }); err != nil {
		return err
	}
	if err := reconcileObject(c, &networkingv1.Ingress{ObjectMeta: c.ObjectMeta(c.Name() + externalNativeSuffix)}, wantNative,
		func() client.Object { return externalNativeIngress(c, host) }); err != nil {
		return err
	}
	return reconcileObject(c, &corev1.Service{ObjectMeta: c.ObjectMeta(c.Name() + externalServiceSuffix)}, wantHTTP || wantNative,
		func() client.Object { return externalService(c) })
}

// reconcileObject applies build() when wanted, and deletes obj otherwise.
func reconcileObject(c *controller.Context, obj client.Object, wanted bool, build func() client.Object) error {
	if !wanted {
		found, err := getOptional(c, obj, obj.GetName())
		if err != nil || !found {
			return err
		}
		return c.Delete(obj)
	}
	desired := build()
	if err := c.Apply(desired); err != nil {
		return fmt.Errorf("apply %T %s: %w", desired, desired.GetName(), err)
	}
	return nil
}

// externalService gives the Ingresses a ClusterIP backend: the client Service
// is headless unless exposed, and the Ingress controller balances over a
// Service's cluster IP.
func externalService(c *controller.Context) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: c.ObjectMeta(c.Name() + externalServiceSuffix),
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: map[string]string{common.LabelCHIName: c.Name()},
			Ports: []corev1.ServicePort{
				{Name: "http", Protocol: corev1.ProtocolTCP, Port: common.HTTPPort, TargetPort: intstr.FromInt32(common.HTTPPort)},
				{Name: "secureclient", Protocol: corev1.ProtocolTCP, Port: common.TCPSecurePort, TargetPort: intstr.FromInt32(common.TCPSecurePort)},
			},
		},
	}
}

func externalHTTPIngress(c *controller.Context, host string) *networkingv1.Ingress {
	ingress := externalIngress(c, c.Name()+externalHTTPSuffix, host, common.HTTPPort)
	if issuer := os.Getenv(common.EnvIngressClusterIssuer); issuer != "" {
		ingress.Annotations = map[string]string{"cert-manager.io/cluster-issuer": issuer}
	}
	ingress.Spec.TLS = []networkingv1.IngressTLS{{Hosts: []string{host}, SecretName: c.Name() + externalTLSSuffix}}
	return ingress
}

func externalNativeIngress(c *controller.Context, host string) *networkingv1.Ingress {
	ingress := externalIngress(c, c.Name()+externalNativeSuffix, host, common.TCPSecurePort)
	ingress.Annotations = map[string]string{"ingress.cilium.io/tls-passthrough": "enabled"}
	return ingress
}

func externalIngress(c *controller.Context, name, host string, port int32) *networkingv1.Ingress {
	prefix := networkingv1.PathTypePrefix
	ingress := &networkingv1.Ingress{
		ObjectMeta: c.ObjectMeta(name),
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{{
				Host: host,
				IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
					Paths: []networkingv1.HTTPIngressPath{{
						Path:     "/",
						PathType: &prefix,
						Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{
							Name: c.Name() + externalServiceSuffix,
							Port: networkingv1.ServiceBackendPort{Number: port},
						}},
					}},
				}},
			}},
		},
	}
	if class := os.Getenv(common.EnvIngressClass); class != "" {
		ingress.Spec.IngressClassName = &class
	}
	return ingress
}
