/*
Copyright 2016 The Kubernetes Authors.

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

package spc

import (
	"fmt"
	"math/rand"
	"strconv"

	"github.com/StackPointCloud/stackpoint-sdk-go/pkg/stackpointio"
	"github.com/golang/glog"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	apimachinery "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	"k8s.io/autoscaler/cluster-autoscaler/clusterstate"
	"k8s.io/autoscaler/cluster-autoscaler/utils/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/kubernetes/plugin/pkg/scheduler/schedulercache"
)

// NodeClass contains information of the type of node
type NodeClass struct {
	// Type is the string used by the cloudprovider to identify the type
	Type string
	// CPU is an integer number of cores
	CPU string
	// MemoryMB is an integer number of MB of memory
	MemoryMB string
}

// NodeGroup implements NodeGroup
type NodeGroup struct {
	id           string
	stackpointID int
	class        NodeClass
	maxSize      int
	minSize      int
	manager      *NodeManager
}

// NewSpcNodeGroup creates a new node group from a stackpoint.io NodePool
func NewSpcNodeGroup(prefix string, nodepool stackpointio.NodePool, manager *NodeManager) *NodeGroup {
	return &NodeGroup{
		id:           prefix + nodepool.InstanceID,
		stackpointID: nodepool.PrimaryKey,
		class: NodeClass{
			Type:     nodepool.Size,
			CPU:      nodepool.CPU,
			MemoryMB: nodepool.Memory,
		},
		maxSize: nodepool.MaxCount,
		minSize: nodepool.MinCount,
		manager: manager,
	}
}

// MaxSize returns maximum size of the node group.
func (sng NodeGroup) MaxSize() int {
	return sng.maxSize
}

// MinSize returns minimum size of the node group.
func (sng NodeGroup) MinSize() int {
	return sng.minSize
}

// TargetSize returns the current target size of the node group.
func (sng NodeGroup) TargetSize() (int, error) {
	count := 0
	for _, node := range sng.manager.nodes {
		if node.Group == sng.Id() && node.State == "running" {
			count++
		}
	}
	return count, nil
}

// IncreaseSize increases the size of the node group. To delete a node you need
// to explicitly name it and use DeleteNode. This function should wait until
// node group size is updated.
func (sng NodeGroup) IncreaseSize(delta int) error {
	_, err := sng.manager.IncreaseSize(delta, sng.class.Type, &sng)
	return err
}

// DecreaseTargetSize decreases the target size of the node group. This function
// doesn't permit to delete any existing node and can be used only to reduce the
// request for new nodes that have not been yet fulfilled. Delta should be negative.
func (sng NodeGroup) DecreaseTargetSize(delta int) error {
	if delta >= 0 {
		return fmt.Errorf("size decrease must be negative")
	}
	return nil
}

// DeleteNodes deletes nodes from this node group. Error is returned either on
// failure or if the given node doesn't belong to this node group. This function
// should wait until node group size is updated.
func (sng NodeGroup) DeleteNodes(nodes []*apiv1.Node) error {
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		instanceID, err := instanceIDForNode(node)
		if err != nil {
			glog.V(2).Info(err.Error())
		} else {
			ids = append(ids, instanceID)
		}
	}
	_, err := sng.manager.DeleteNodes(ids)
	return err
}

// returns empty string for missing values so long as the hostname label is ok
func labelValueForNode(key string, node *apiv1.Node) (string, error) {
	labels := node.ObjectMeta.Labels
	value, ok := labels[key]
	if !ok {
		_, reallyOK := labels["kubernetes.io/hostname"]
		if reallyOK {
			// the node is labelled reasonably, just return an empty string for the value
			return "", nil
			// errorResp = fmt.Errorf("Unable to find label [%s] for node [%s]", key, hostname)
		}
		return "", fmt.Errorf("Unable to find label [%s] for a node without 'kubernetes.io/hostname' label", key)
	}
	return value, nil
}

func instanceIDForNode(node *apiv1.Node) (string, error) {
	return labelValueForNode("stackpoint.io/instance_id", node)
}

func nodeGroupNameForNode(node *apiv1.Node) (string, error) {
	return labelValueForNode("stackpoint.io/node_group", node)
}

// TemplateNodeInfo returns a schedulercache.NodeInfo structure of an empty
// (as if just started) node. This will be used in scale-up simulations to
// predict what would a new node look like if a node group was expanded. The returned
// NodeInfo is expected to have a fully populated Node object, with all of the labels,
// capacity and allocatable information as well as all pods that are started on
// the node by default, using manifest (most likely only kube-proxy). Implementation optional.
func (sng NodeGroup) TemplateNodeInfo() (*schedulercache.NodeInfo, error) {

	// obtain template for node group
	// assign node properties from template
	// assign node to a new NodeInfo

	node := apiv1.Node{}
	nodeName := fmt.Sprintf("%s-template-%d", sng.Id(), rand.Int63())

	node.ObjectMeta = metav1.ObjectMeta{
		Name:     nodeName,
		SelfLink: fmt.Sprintf("/api/v1/nodes/%s", nodeName),
		Labels:   map[string]string{},
	}

	if sng.class.CPU != "" && sng.class.MemoryMB != "" {
		cpu, err1 := strconv.ParseInt(sng.class.CPU, 10, 64)
		memoryMB, err2 := strconv.ParseInt(sng.class.MemoryMB, 10, 64)
		if err1 == nil && err2 == nil {
			capacity := apiv1.ResourceList{}
			capacity[apiv1.ResourcePods] = *resource.NewQuantity(110, resource.DecimalSI)
			capacity[apiv1.ResourceCPU] = *resource.NewQuantity(cpu, resource.DecimalSI)
			capacity[apiv1.ResourceMemory] = *resource.NewQuantity(memoryMB*1024*1024, resource.DecimalSI)

			node.Status = apiv1.NodeStatus{
				Capacity:    capacity,
				Allocatable: capacity,
			}
		}
	}

	// NodeLabels
	// node.Labels = cloudprovider.JoinStringMaps(node.Labels, ...
	// node.Spec.Taints =

	node.Status.Conditions = cloudprovider.BuildReadyConditions()

	nodeInfo := schedulercache.NewNodeInfo(cloudprovider.BuildKubeProxy(sng.Id()))
	nodeInfo.SetNode(&node)

	allocatable := nodeInfo.AllocatableResource()
	glog.V(5).Infof("Setting template nodeInfo resources milliCPU: %v, memory: %v, pods: %v ",
		allocatable.MilliCPU, allocatable.Memory, allocatable.AllowedPodNumber)

	return nodeInfo, nil
}

// Exist checks if the node group really exists on the cloud provider side. Allows to tell the
// theoretical node group from the real one. Implementation required.
func (sng NodeGroup) Exist() bool {
	// nodegroups are refreshed regularly, so if it has a remote id, it should be real without requiring a remote call
	return sng.stackpointID != 0

	// _, err := sng.manager.clusterClient.getNodePool(sng.ID)
	// if err != nil {
	// 	return false
	// }
	// return true
}

// Create creates the node group on the cloud provider side. Implementation optional.
func (sng NodeGroup) Create() error {
	return cloudprovider.ErrNotImplemented
}

// Delete deletes the node group on the cloud provider side.
// This will be executed only for autoprovisioned node groups, once their size drops to 0.
// Implementation optional.
func (sng NodeGroup) Delete() error {
	return cloudprovider.ErrNotImplemented
}

// Autoprovisioned returns true if the node group is autoprovisioned. An autoprovisioned group
// was created by CA and can be deleted when scaled to 0.
func (sng NodeGroup) Autoprovisioned() bool {
	return false
}

// Id returns an unique identifier of the node group.
func (sng NodeGroup) Id() string {
	return sng.id
}

// Debug returns a string containing all information regarding this node group.
func (sng NodeGroup) Debug() string {
	var msg string
	target, err := sng.TargetSize()
	if err != nil {
		msg = err.Error()
	}
	return fmt.Sprintf("%s [%d] (%d:%d) (%d) %s", sng.Id(), sng.stackpointID, sng.MinSize(), sng.MaxSize(), target, msg)
}

// Nodes returns a list of all nodes that belong to this node group.
func (sng NodeGroup) Nodes() ([]string, error) {

	// earlier comment ...
	// return value is a set of strings
	// for gce, this is "fmt.Sprintf("gce://%s/%s/%s", project, zone, name))"
	// for aws, this is Instance.InstanceId, something like "i-04a211e5f5c755e64"
	// for azure, this is "azure:////" + fixEndiannessUUID(string(strings.ToUpper(*instance.VirtualMachineScaleSetVMProperties.VMID)))""

	var names []string

	labelSelection := "stackpoint.io/node_group=" + sng.Id()
	glog.V(5).Infof("Querying nodes for label %s", labelSelection)

	options := apimachinery.ListOptions{LabelSelector: labelSelection}
	nodeList, err := sng.manager.k8sClient.CoreV1().Nodes().List(options)
	if err != nil {
		return nil, err
	}
	glog.V(5).Infof("Retrieved %d nodes for node query", len(nodeList.Items))
	for _, node := range nodeList.Items {
		names = append(names, clusterstate.GetRegistrationName(&node))
	}

	return names, nil
}

// SpcCloudProvider implements CloudProvider
type SpcCloudProvider struct {
	spcClient       *StackpointClusterClient
	spcManager      *NodeManager
	resourceLimiter *cloudprovider.ResourceLimiter
}

// BuildSpcCloudProvider builds CloudProvider implementation for stackpointio.
func BuildSpcCloudProvider(spcClient *StackpointClusterClient, configNamespace string, specs []string, resourceLimiter *cloudprovider.ResourceLimiter) (*SpcCloudProvider, error) {
	if spcClient == nil {
		return nil, fmt.Errorf("ClusterClient is nil")
	}

	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("Cannot get the in-cluster config, %v", err)
	}

	k8sClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("Cannot create the in-cluster client, %v", err)
	}
	manager := CreateNodeManager(spcClient, k8sClient, configNamespace)

	spc := &SpcCloudProvider{
		spcClient:       spcClient,
		spcManager:      &manager,
		resourceLimiter: resourceLimiter,
	}

	return spc, nil
}

// Name returns name of the cloud provider.
func (spc *SpcCloudProvider) Name() string {
	return "spc"
}

// NodeGroups returns all node groups configured for this cloud provider.
func (spc *SpcCloudProvider) NodeGroups() []cloudprovider.NodeGroup {
	var result []cloudprovider.NodeGroup
	for _, nodeGroup := range spc.spcManager.nodeGroups {
		result = append(result, *nodeGroup)
	}
	return result
}

// NodeGroupForNode returns the node group for the given node
func (spc *SpcCloudProvider) NodeGroupForNode(node *apiv1.Node) (cloudprovider.NodeGroup, error) {

	nodeGroupName, err := nodeGroupNameForNode(node)
	if err != nil {
		glog.V(2).Infof("Node not manageable <%s/%s>, can't get nodeGroupName, %v", node.ObjectMeta.Name, node.Spec.ExternalID, err)
		return nil, err
	}
	if nodeGroupName == "" {
		glog.V(5).Infof("Node not manageable %s, nodeGroupName is empty", node.Spec.ExternalID)
		return nil, nil // fmt.Errorf("Node not manageable <%s>, nodeGroupName is empty", node.Name)
	}
	for _, nodeGroup := range spc.spcManager.nodeGroups {
		if nodeGroup.Id() == nodeGroupName {
			glog.V(5).Infof("Matched nodeGroupName %s to node %s", nodeGroupName, node.Spec.ExternalID)
			return nodeGroup, nil
		}
	}

	// Failed to match nodeGroupName autoscaling-spcu22235o-pool-1 of node spcu22235o-worker-1111010
	glog.V(2).Infof("Failed to match nodeGroupName %s of node %s, marking as empty nodegroup", nodeGroupName, node.Spec.ExternalID)
	return nil, nil // fmt.Errorf("Failed to matched nodeGroupName %s to any nodepool", nodeGroupName)
}

// Pricing returns pricing model for this cloud provider or error if not available.
// not implemented
func (spc *SpcCloudProvider) Pricing() (cloudprovider.PricingModel, errors.AutoscalerError) {
	return nil, cloudprovider.ErrNotImplemented
}

// GetAvailableMachineTypes get all machine types that can be requested from the cloud provider.
func (spc *SpcCloudProvider) GetAvailableMachineTypes() ([]string, error) {
	return []string{}, cloudprovider.ErrNotImplemented
}

// NewNodeGroup builds a theoretical node group based on the node definition provided. The node group is not automatically
// created on the cloud provider side. The node group is not returned by NodeGroups() until it is created.
// Implementation optional.
func (spc *SpcCloudProvider) NewNodeGroup(machineType string, labels map[string]string, systemLabels map[string]string, extraResources map[string]resource.Quantity) (cloudprovider.NodeGroup, error) {
	return nil, cloudprovider.ErrNotImplemented
}

// GetResourceLimiter returns struct containing limits (max, min) for resources (cores, memory etc.).
func (spc *SpcCloudProvider) GetResourceLimiter() (*cloudprovider.ResourceLimiter, error) {
	if spc.resourceLimiter == nil {
		return nil, fmt.Errorf("Resource limiter is not configured")
	}
	return spc.resourceLimiter, nil
}

// Cleanup not needed here yet
func (spc *SpcCloudProvider) Cleanup() error {
	return nil // cloudprovider.ErrNotImplemented
}

// Refresh the cloud provider view of nodes and nodeGroups
func (spc *SpcCloudProvider) Refresh() error {
	return spc.spcManager.Refresh()
}
