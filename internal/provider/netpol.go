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
	"strings"

	chiv1 "github.com/altinity/clickhouse-operator/pkg/apis/clickhouse.altinity.com/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-altinity-clickhouse/internal/common"
)

// serviceAccountNamespaceFile holds the namespace of the pod the provider runs in.
const serviceAccountNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

// reconcileNetworkPolicies applies the NetworkPolicies of the Instance: the
// client policy (see clientPolicy), Keeper's, which authenticates no one (see
// keeperPolicy), and, with backup enabled, the clickhouse-backup sidecar's.
func reconcileNetworkPolicies(c *controller.Context, chi *chiv1.ClickHouseInstallation) error {
	if err := reconcilePolicy(c, c.Name()+common.PlaintextPolicySuffix, false, nil); err != nil {
		return err
	}
	if err := reconcilePolicy(c, c.Name()+common.ClientPolicySuffix, true, func(c *controller.Context) (*networkingv1.NetworkPolicy, error) {
		return clientPolicy(c, chi)
	}); err != nil {
		return err
	}
	if err := reconcilePolicy(c, c.Name()+common.KeeperPolicySuffix, usesKeeper(c), keeperPolicy); err != nil {
		return err
	}
	return reconcilePolicy(c, c.Name()+common.BackupAPIPolicySuffix, backupEnabled(c), backupAPIPolicy)
}

// reconcilePolicy applies the named NetworkPolicy when wanted, and removes
// it otherwise.
func reconcilePolicy(c *controller.Context, name string, wanted bool, build func(*controller.Context) (*networkingv1.NetworkPolicy, error)) error {
	if !wanted {
		found, err := getOptional(c, &networkingv1.NetworkPolicy{}, name)
		if err != nil || !found {
			return err
		}
		return c.Delete(&networkingv1.NetworkPolicy{ObjectMeta: c.ObjectMeta(name)})
	}
	policy, err := build(c)
	if err != nil {
		return err
	}
	if err := c.Apply(policy); err != nil {
		return fmt.Errorf("apply NetworkPolicy %s: %w", name, err)
	}
	return nil
}

// appliedTLSRequired reports whether chi was applied in the required TLS
// mode: its client Service carries the secure ports only. (Not an annotation:
// the operator copies CHI annotations onto the pods, restarting them.)
func appliedTLSRequired(chi *chiv1.ClickHouseInstallation) bool {
	tmpl := appliedServiceTemplate(chi)
	if tmpl == nil {
		return false
	}
	secure, plaintext := false, false
	for _, port := range tmpl.Spec.Ports {
		switch port.Port {
		case common.HTTPSPort, common.TCPSecurePort:
			secure = true
		case common.HTTPPort, common.TCPPort:
			plaintext = true
		}
	}
	return secure && !plaintext
}

// appliedServiceTemplate returns chi's client Service template, nil when the
// operator's default Service applies.
func appliedServiceTemplate(chi *chiv1.ClickHouseInstallation) *chiv1.ServiceTemplate {
	if chi.Spec.Templates == nil {
		return nil
	}
	for i := range chi.Spec.Templates.ServiceTemplates {
		if chi.Spec.Templates.ServiceTemplates[i].Name == common.ServiceTemplateName {
			return &chi.Spec.Templates.ServiceTemplates[i]
		}
	}
	return nil
}

// keeperPolicy limits Keeper to the Instance's ClickHouse pods (client
// ports), its own nodes (client and Raft ports) and the operator.
func keeperPolicy(c *controller.Context) (*networkingv1.NetworkPolicy, error) {
	operator, err := operatorPeer()
	if err != nil {
		return nil, err
	}
	clickhouse := metav1.LabelSelector{MatchLabels: map[string]string{common.LabelCHIName: c.Name()}}
	keeper := metav1.LabelSelector{MatchLabels: map[string]string{common.LabelCHKName: keeperCRName(c.Name())}}
	return &networkingv1.NetworkPolicy{
		ObjectMeta: c.ObjectMeta(c.Name() + common.KeeperPolicySuffix),
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: keeper,
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					From:  []networkingv1.NetworkPolicyPeer{{PodSelector: &clickhouse}},
					Ports: tcpPorts(common.KeeperPort, common.KeeperSecurePort),
				},
				{
					From:  []networkingv1.NetworkPolicyPeer{{PodSelector: &keeper}},
					Ports: tcpPorts(common.KeeperPort, common.KeeperSecurePort, common.KeeperRaftPort),
				},
				{From: []networkingv1.NetworkPolicyPeer{operator}},
			},
		},
	}, nil
}

// backupAPIPolicy limits the clickhouse-backup sidecar REST API (port 7171) to
// the backup and restore Jobs of this CHI.
func backupAPIPolicy(c *controller.Context) (*networkingv1.NetworkPolicy, error) {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: c.ObjectMeta(c.Name() + common.BackupAPIPolicySuffix),
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{common.LabelCHIName: c.Name()}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{common.LabelBackupCHIName: c.Name()}},
				}},
				Ports: tcpPorts(common.BackupAPIPort),
			}},
		},
	}, nil
}

// operatorPeer selects the Altinity operator pods: CLICKHOUSE_OPERATOR_NAMESPACE
// (the provider's own namespace by default) and CLICKHOUSE_OPERATOR_POD_LABELS.
func operatorPeer() (networkingv1.NetworkPolicyPeer, error) {
	namespace := os.Getenv(common.EnvOperatorNamespace)
	if namespace == "" {
		data, err := os.ReadFile(serviceAccountNamespaceFile)
		if err != nil {
			return networkingv1.NetworkPolicyPeer{}, fmt.Errorf("operator namespace unknown: set %s", common.EnvOperatorNamespace)
		}
		namespace = strings.TrimSpace(string(data))
	}
	selector := os.Getenv(common.EnvOperatorPodLabels)
	if selector == "" {
		selector = common.DefaultOperatorPodLabels
	}
	podLabels, err := labels.ConvertSelectorToLabelsMap(selector)
	if err != nil {
		return networkingv1.NetworkPolicyPeer{}, fmt.Errorf("parse %s: %w", common.EnvOperatorPodLabels, err)
	}
	return networkingv1.NetworkPolicyPeer{
		NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{corev1.LabelMetadataName: namespace}},
		PodSelector:       &metav1.LabelSelector{MatchLabels: podLabels},
	}, nil
}

func tcpPorts(ports ...int) []networkingv1.NetworkPolicyPort {
	tcp := corev1.ProtocolTCP
	out := make([]networkingv1.NetworkPolicyPort, 0, len(ports))
	for _, p := range ports {
		port := intstr.FromInt32(int32(p))
		out = append(out, networkingv1.NetworkPolicyPort{Protocol: &tcp, Port: &port})
	}
	return out
}
