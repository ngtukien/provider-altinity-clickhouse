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
	"net/netip"
	"slices"
	"strings"

	chiv1 "github.com/altinity/clickhouse-operator/pkg/apis/clickhouse.altinity.com/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-altinity-clickhouse/definition/components"
	"github.com/openeverest/provider-altinity-clickhouse/internal/common"
)

// allProtocols are the client protocols, all open by default.
var allProtocols = []string{common.ProtocolHTTP, common.ProtocolNative}

// networkParams returns the engine's network parameters, nil when unset.
func networkParams(c *controller.Context) *components.NetworkParameters {
	var params components.ClickHouseParameters
	_ = c.TryDecodeComponentParameters(c.Instance().Spec.Components[common.ComponentEngine], &params)
	return params.Network
}

// clientProtocols returns the client protocols the Instance opens.
func clientProtocols(c *controller.Context) []string {
	if p := networkParams(c); p != nil && p.Protocols != "" && p.Protocols != common.ProtocolAll {
		return []string{p.Protocols}
	}
	return allProtocols
}

// splitList returns the non-empty, trimmed items of a comma-separated list.
func splitList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// clientPolicy limits the ClickHouse pods' ports. The open protocols are
// reachable from anyone, or, with allowed sources, from those and the
// Instance's namespace; a LoadBalancer or NodePort Service opens them to
// anyone again, since external traffic may reach the pods from a node's
// address. The Instance's own pods,
// which replicate and run distributed queries, and the operator reach every
// port; anyone reaches the metrics endpoint.
//
// Like the TLS mode and the Service, it follows the applied CHI, so a change
// held for maintenance takes effect together with the rest of it.
func clientPolicy(c *controller.Context, chi *chiv1.ClickHouseInstallation) (*networkingv1.NetworkPolicy, error) {
	operator, err := operatorPeer()
	if err != nil {
		return nil, err
	}
	pods := metav1.LabelSelector{MatchLabels: map[string]string{common.LabelCHIName: c.Name()}}
	ingress := []networkingv1.NetworkPolicyIngressRule{
		{Ports: tcpPorts(common.MetricsPort)},
		{
			From: []networkingv1.NetworkPolicyPeer{{PodSelector: &pods}, operator},
			Ports: tcpPorts(common.HTTPPort, common.TCPPort, common.HTTPSPort, common.TCPSecurePort,
				common.InterserverHTTPPort, common.InterserverHTTPSPort),
		},
	}

	// The secure ports listen only with TLS on, so outside required TLS both
	// kinds can be open.
	mode := common.TLSModeOptional
	if appliedTLSRequired(chi) {
		mode = common.TLSModeRequired
	}
	var open []int
	for _, port := range clientPorts(mode, clientProtocols(c)) {
		open = append(open, port)
	}
	slices.Sort(open)

	var from []networkingv1.NetworkPolicyPeer // none: from anyone
	if !appliedExposed(chi) {
		from = allowedPeers(networkParams(c))
	}
	ingress = append(ingress, networkingv1.NetworkPolicyIngressRule{From: from, Ports: tcpPorts(open...)})

	return &networkingv1.NetworkPolicy{
		ObjectMeta: c.ObjectMeta(c.Name() + common.ClientPolicySuffix),
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: pods,
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress:     ingress,
		},
	}, nil
}

// allowedPeers returns the pods of the Instance's namespace and the allowed
// sources of p, or none, letting anyone in, without allowed sources.
func allowedPeers(p *components.NetworkParameters) []networkingv1.NetworkPolicyPeer {
	if p == nil || p.AllowedSources == nil {
		return nil
	}
	namespaces, cidrs := splitList(p.AllowedSources.Namespaces), splitList(p.AllowedSources.CIDRs)
	if len(namespaces)+len(cidrs) == 0 {
		return nil
	}
	// A pod selector alone selects the policy's own namespace.
	peers := []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}}
	for _, ns := range namespaces {
		peers = append(peers, networkingv1.NetworkPolicyPeer{
			NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{corev1.LabelMetadataName: ns}},
		})
	}
	for _, cidr := range cidrs {
		peers = append(peers, networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{CIDR: cidr}})
	}
	return peers
}

// appliedExposed reports whether chi's client Service is a LoadBalancer or
// a NodePort.
func appliedExposed(chi *chiv1.ClickHouseInstallation) bool {
	tmpl := appliedServiceTemplate(chi)
	return tmpl != nil && (tmpl.Spec.Type == corev1.ServiceTypeLoadBalancer || tmpl.Spec.Type == corev1.ServiceTypeNodePort)
}

// validateNetwork checks the network parameters.
func validateNetwork(path *field.Path, p *components.NetworkParameters) field.ErrorList {
	if p == nil {
		return nil
	}
	var errs field.ErrorList
	protocols := []string{common.ProtocolAll, common.ProtocolHTTP, common.ProtocolNative}
	if p.Protocols != "" && !slices.Contains(protocols, p.Protocols) {
		errs = append(errs, field.NotSupported(path.Child("protocols"), p.Protocols, protocols))
	}
	if p.AllowedSources == nil {
		return errs
	}
	sources := path.Child("allowedSources")
	for _, ns := range splitList(p.AllowedSources.Namespaces) {
		for _, msg := range validation.IsDNS1123Label(ns) {
			errs = append(errs, field.Invalid(sources.Child("namespaces"), ns, msg))
		}
	}
	for _, cidr := range splitList(p.AllowedSources.CIDRs) {
		prefix, err := netip.ParsePrefix(cidr)
		switch {
		case err != nil:
			errs = append(errs, field.Invalid(sources.Child("cidrs"), cidr, "must be a CIDR such as 10.0.5.0/24"))
		case prefix.Masked() != prefix:
			errs = append(errs, field.Invalid(sources.Child("cidrs"), cidr, "must not have host bits set; use "+prefix.Masked().String()))
		}
	}
	return errs
}
