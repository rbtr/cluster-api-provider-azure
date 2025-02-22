//go:build e2e
// +build e2e

/*
Copyright 2023 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v4"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	infrav1 "sigs.k8s.io/cluster-api-provider-azure/api/v1beta1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	expv1 "sigs.k8s.io/cluster-api/exp/api/v1beta1"
)

type AKSClusterClassInput struct {
	Cluster                    *clusterv1.Cluster
	MachinePool                *expv1.MachinePool
	WaitIntervals              []interface{}
	WaitUpgradeIntervals       []interface{}
	KubernetesVersionUpgradeTo string
}

func AKSClusterClassSpec(ctx context.Context, inputGetter func() AKSClusterClassInput) {
	input := inputGetter()

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	Expect(err).NotTo(HaveOccurred())

	managedClustersClient, err := armcontainerservice.NewManagedClustersClient(getSubscriptionID(Default), cred, nil)
	Expect(err).NotTo(HaveOccurred())

	agentPoolsClient, err := armcontainerservice.NewAgentPoolsClient(getSubscriptionID(Default), cred, nil)
	Expect(err).NotTo(HaveOccurred())

	mgmtClient := bootstrapClusterProxy.GetClient()
	Expect(mgmtClient).NotTo(BeNil())

	amcp := &infrav1.AzureManagedControlPlane{}
	err = mgmtClient.Get(ctx, types.NamespacedName{
		Namespace: input.Cluster.Spec.ControlPlaneRef.Namespace,
		Name:      input.Cluster.Spec.ControlPlaneRef.Name,
	}, amcp)
	Expect(err).NotTo(HaveOccurred())

	By("Editing the AzureManagedMachinePoolTemplate to change the scale down mode")
	ammpt := &infrav1.AzureManagedMachinePoolTemplate{}

	// TODO: We are hard-coding the ammpt name suffix since to get the name from
	// the ClusterClass, we have to upgrade CAPI to v1.6.0. Fix this once CAPI
	// v1.6.0 is released.
	// CAPI v1.6.0 Bump PR: https://github.com/kubernetes-sigs/cluster-api-provider-azure/pull/4182
	Eventually(func(g Gomega) {
		err = mgmtClient.Get(ctx, types.NamespacedName{
			Namespace: input.Cluster.Namespace,
			Name:      input.Cluster.Name + "-pool0",
		}, ammpt)
		Expect(err).NotTo(HaveOccurred())
		ammpt.Spec.Template.Spec.ScaleDownMode = ptr.To("Deallocate")
		g.Expect(mgmtClient.Update(ctx, ammpt)).To(Succeed())
	}, inputGetter().WaitIntervals...).Should(Succeed())

	ammp := &infrav1.AzureManagedMachinePool{}

	Eventually(func(g Gomega) {
		err = mgmtClient.Get(ctx, types.NamespacedName{
			Namespace: input.MachinePool.Spec.Template.Spec.InfrastructureRef.Namespace,
			Name:      input.MachinePool.Spec.Template.Spec.InfrastructureRef.Name,
		}, ammp)
		Expect(err).NotTo(HaveOccurred())
		g.Expect(ammp.Spec.ScaleDownMode).To(Equal(ptr.To("Deallocate")))
	}, inputGetter().WaitIntervals...).Should(Succeed())

	Eventually(func(g Gomega) {
		resp, err := agentPoolsClient.Get(ctx, amcp.Spec.ResourceGroupName, amcp.Name, *ammp.Spec.Name, nil)
		g.Expect(err).NotTo(HaveOccurred())
		agentPool := resp.AgentPool
		g.Expect(agentPool.Properties).NotTo(BeNil())
		g.Expect(agentPool.Properties.ScaleDownMode).NotTo(BeNil())
		g.Expect(*agentPool.Properties.ScaleDownMode).To(Equal(armcontainerservice.ScaleDownModeDeallocate))
	}, input.WaitIntervals...).Should(Succeed())

	By("Upgrading the cluster topology version")
	Eventually(func(g Gomega) {
		input.Cluster.Spec.Topology.Version = input.KubernetesVersionUpgradeTo
		g.Expect(mgmtClient.Update(ctx, input.Cluster)).To(Succeed())
	}, inputGetter().WaitIntervals...).Should(Succeed())

	Eventually(func(g Gomega) {
		resp, err := managedClustersClient.Get(ctx, amcp.Spec.ResourceGroupName, amcp.Name, nil)
		g.Expect(err).NotTo(HaveOccurred())
		aksCluster := resp.ManagedCluster
		g.Expect(aksCluster.Properties).NotTo(BeNil())
		g.Expect(aksCluster.Properties.KubernetesVersion).NotTo(BeNil())
		g.Expect("v" + *aksCluster.Properties.KubernetesVersion).To(Equal(input.KubernetesVersionUpgradeTo))
		g.Expect(aksCluster.Properties.ProvisioningState).To(Equal(ptr.To("Succeeded")))
	}, input.WaitUpgradeIntervals...).Should(Succeed())

	By("Ensuring the upgrade is reflected in the amcp")
	Eventually(func(g Gomega) {
		g.Expect(mgmtClient.Get(ctx, types.NamespacedName{
			Namespace: input.Cluster.Spec.ControlPlaneRef.Namespace,
			Name:      input.Cluster.Spec.ControlPlaneRef.Name,
		}, amcp)).To(Succeed())
		g.Expect(amcp.Spec.Version).To(Equal(input.KubernetesVersionUpgradeTo))
	}, input.WaitIntervals...).Should(Succeed())
}
