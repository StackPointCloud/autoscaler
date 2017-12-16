package spc

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/StackPointCloud/stackpoint-sdk-go/pkg/stackpointio"
	"k8s.io/client-go/kubernetes"

	"github.com/golang/glog"
)

const (
	spcAPIRequestLimit     time.Duration = 60 * time.Second
	scalingPollingInterval time.Duration = 60 * time.Second
	scalingTimeout         time.Duration = 20 * time.Minute
	nodePoolConfigName     string        = "stackpoint-node-pools"
)

// StackpointClusterClient is a StackPointCloud API client for a particular cluster
type StackpointClusterClient struct {
	organization int
	id           int
	apiClient    *stackpointio.APIClient
}

// CreateClusterClient creates a ClusterClient from environment
// variables CLUSTER_API_TOKEN, SPC_BASE_API_URL, ORGANIZATION_ID, CLUSTER_ID
func CreateClusterClient() (*StackpointClusterClient, error) {

	token := os.Getenv("CLUSTER_API_TOKEN")
	endpoint := os.Getenv("SPC_API_BASE_URL")
	organizationID := os.Getenv("ORGANIZATION_ID")
	clusterID := os.Getenv("CLUSTER_ID")

	if token == "" {
		return nil, fmt.Errorf("Environment variable CLUSTER_API_TOKEN not defined")
	}
	if endpoint == "" {
		return nil, fmt.Errorf("Environment variable SPC_API_BASE_URL not defined")
	}
	orgPk, err := strconv.Atoi(organizationID)
	if err != nil {
		return nil, fmt.Errorf("Bad environment variable for organizationID [%s]", organizationID)
	}
	clusterPk, err := strconv.Atoi(clusterID)
	if err != nil {
		return nil, fmt.Errorf("Bad environment variable for clusterID [%s]", clusterID)
	}

	apiClient := stackpointio.NewClient(token, endpoint)
	glog.V(5).Infof("Using stackpoint io api server [%s], organization [%s], cluster [%s]", endpoint, organizationID, clusterID)

	clusterClient := &StackpointClusterClient{
		organization: orgPk,
		id:           clusterPk,
		apiClient:    apiClient,
	}
	return clusterClient, nil
}

func (cClient *StackpointClusterClient) getOrganization() int { return cClient.organization }

func (cClient *StackpointClusterClient) getID() int { return cClient.id }

func (cClient *StackpointClusterClient) getNodes() ([]stackpointio.Node, error) {
	return cClient.apiClient.GetNodes(cClient.organization, cClient.id)
}

func (cClient *StackpointClusterClient) getNodePools() ([]stackpointio.NodePool, error) {
	return cClient.apiClient.GetNodePools(cClient.organization, cClient.id)
}

func (cClient *StackpointClusterClient) getNodePool(nodePoolID int) (stackpointio.NodePool, error) {
	return cClient.apiClient.GetNodePool(cClient.organization, cClient.id, nodePoolID)
}

func (cClient *StackpointClusterClient) addNodes(nodePoolID int, requestNodes stackpointio.NodeAdd) ([]stackpointio.Node, error) {

	// use old endpoint
	newNodes, err := cClient.apiClient.AddNodes(cClient.organization, cClient.id, requestNodes)

	// new nodepool endpoint
	// newNodes, err := cClient.apiClient.AddNodesToNodePool(cClient.organization, cClient.id, nodePoolID, requestNodes)

	if err != nil {
		return nil, err
	}
	return newNodes, nil
}

func (cClient *StackpointClusterClient) deleteNode(nodePK int) ([]byte, error) {
	someResponse, err := cClient.apiClient.DeleteNode(cClient.organization, cClient.id, nodePK)
	if err != nil {
		return nil, err
	}
	return someResponse, nil
}

// NodeManager has a set of nodes and can add or delete them via the StackPointCloud API
type NodeManager struct {
	clusterClient      *StackpointClusterClient
	k8sClient          *kubernetes.Clientset
	configNamespace    string
	nodes              map[string]stackpointio.Node
	nodeGroups         map[string]*NodeGroup
	apiRequestInterval time.Duration
	lastAPIRequestTime time.Time
}

// CreateNodeManager creates a NodeManager
func CreateNodeManager(cluster *StackpointClusterClient, k8sClient *kubernetes.Clientset, configNamespace string) NodeManager {
	manager := NodeManager{
		clusterClient:      cluster,
		k8sClient:          k8sClient,
		configNamespace:    configNamespace,
		nodes:              make(map[string]stackpointio.Node, 0),
		nodeGroups:         make(map[string]*NodeGroup, 0),
		apiRequestInterval: spcAPIRequestLimit,
	}
	manager.Refresh()
	return manager
}

// Size returns the number of nodes which are in a "running" state
func (manager *NodeManager) Size() int {
	var running int
	for _, node := range manager.nodes {
		if node.State == "running" {
			running++
		}
	}
	return running
}

func (manager *NodeManager) countStates() *map[string]int {
	counts := map[string]int{"draft": 0, "building": 0, "provisioned": 0, "running": 0, "deleting": 0, "deleted": 0}
	for _, node := range manager.nodes {
		counts[node.State]++
	}
	return &counts
}

func (manager *NodeManager) addNode(node stackpointio.Node) int {
	manager.nodes[node.InstanceID] = node
	return len(manager.nodes)
}

// Nodes returns the  complete set of stackpointio node instanceIDs
func (manager *NodeManager) Nodes() ([]string, error) {
	var keys []string
	for k := range manager.nodes {
		keys = append(keys, k)
	}
	return keys, nil
}

// NodesForGroupID returns a set of stackpointio node instanceIDs for a given node group
func (manager *NodeManager) NodesForGroupID(group string) ([]string, error) {
	var nodeIDs []string
	glog.V(5).Infof("looking for nodes for %s", group)
	if manager.nodes == nil {
		return nil, fmt.Errorf("node list is nil")
	}
	for _, node := range manager.nodes {
		glog.V(5).Infof("     checking node %s:%s:%s (%s)", node.Name, node.InstanceID, node.PrivateIP, node.Group)
		if node.Group == group {
			nodeIDs = append(nodeIDs, node.InstanceID)
		}
	}
	return nodeIDs, nil
}

// GetNode returns a Node identified by the stackpointio instanceID
func (manager *NodeManager) GetNode(instanceID string) (stackpointio.Node, bool) {
	node, ok := manager.nodes[instanceID]
	return node, ok
}

// GetNodePK returns a Node identified by the stackpointio primaryKey
func (manager *NodeManager) GetNodePK(nodePK int) (stackpointio.Node, bool) {
	for _, node := range manager.nodes {
		if node.PrimaryKey == nodePK {
			return node, true
		}
	}
	return stackpointio.Node{}, false
}

// Refresh updates the set of nodegroups and nodes from the stackpointcloud api
func (manager *NodeManager) Refresh() error {
	timepoint := time.Now()
	if timepoint.Sub(manager.lastAPIRequestTime) < manager.apiRequestInterval {
		return nil
	}
	manager.lastAPIRequestTime = timepoint
	glog.V(5).Infof("Updating cluster info, organizationID %d, clusterID %d", manager.clusterClient.getOrganization(), manager.clusterClient.getID())

	groupErr := manager.updateNodeGroups()
	if groupErr != nil {
		return groupErr
	}
	// nodeErr := manager.updateNodes()
	// if nodeErr != nil {
	// 	return nodeErr
	// }
	return nil
}

// updateNodes refreshes the state of the current nodes in the clusterClient
func (manager *NodeManager) updateNodes() error {

	clusterNodes, err := manager.clusterClient.getNodes()
	if err != nil {
		return err
	}
	for _, clusterNode := range clusterNodes {
		localNode, ok := manager.nodes[clusterNode.InstanceID]
		if ok {
			if localNode.State != clusterNode.State {
				glog.V(5).Infof("Node state change, nodeID %s, oldState %s, newState %s", clusterNode.InstanceID, localNode.State, clusterNode.State)
			}
		} else {
			glog.V(5).Infof("New node found, nodeID %s, newState %s", clusterNode.InstanceID, clusterNode.State)
		}
		manager.nodes[clusterNode.InstanceID] = clusterNode
	}
	if len(clusterNodes) < len(manager.nodes) {
		glog.V(2).Info("Remote node count is too small, remote %d vs. local %d", len(clusterNodes), len(manager.nodes))
		reconciliationSet := make(map[string]int)
		for index, clusterNode := range clusterNodes {
			reconciliationSet[clusterNode.InstanceID] = index
		}
		for instanceID := range manager.nodes {
			_, ok := reconciliationSet[instanceID]
			if !ok {
				glog.V(2).Info("Local node does not exist in cluster, removing nodeID %s", instanceID)
				delete(manager.nodes, instanceID)
			}
		}
	}
	return nil
}

func getPoolInstanceIDName(clusterNode stackpointio.Node) string {
	prefix := "autoscaling-"
	return strings.TrimPrefix(clusterNode.Group, prefix)
}

// updateNodeGroups retrieves node group definitions from stackpointio and synchronizes the local node definitions
func (manager *NodeManager) updateNodeGroups() error {

	prefix := "autoscaling-"

	spcNodePools, err := manager.getStackpointNodePools()
	if err != nil {
		return fmt.Errorf("Cannot retrieve nodepool definitions, %v", err)
	}

	remoteAutoscaledGroups := make(map[string]*NodeGroup)
	for _, pool := range spcNodePools {
		if pool.Autoscaled {
			remoteAutoscaledGroups[pool.InstanceID] = NewSpcNodeGroup(prefix, pool, manager)
			glog.V(2).Infof("Updating autoscaled node group %s", pool.InstanceID)
		}
	}
	manager.nodeGroups = remoteAutoscaledGroups
	remoteAutoscaledNodes := make(map[string]stackpointio.Node)
	if len(manager.nodeGroups) > 0 {
		allClusterNodes, err := manager.clusterClient.getNodes()
		if err != nil {
			return err
		}
		for _, clusterNode := range allClusterNodes {
			poolInstanceID := getPoolInstanceIDName(clusterNode)
			if poolInstanceID != "" && manager.nodeGroups[poolInstanceID] != nil {
				localNode, ok := manager.nodes[clusterNode.InstanceID]
				if ok {
					if localNode.State != clusterNode.State {
						glog.V(5).Infof("Node state change, nodeID %s, oldState %s, newState %s", clusterNode.InstanceID, localNode.State, clusterNode.State)
					}
				} else {
					glog.V(5).Infof("New node found, nodeID %s, newState %s", clusterNode.InstanceID, clusterNode.State)
				}
				remoteAutoscaledNodes[clusterNode.InstanceID] = clusterNode
			}
		}
	}
	manager.nodes = remoteAutoscaledNodes

	return nil
}

// getStackpointNodePools gets the set of node pools for this particular cluster, possilbly
// including nodepools that are autoscaled and possbly pools that are not autoscaled.
func (manager *NodeManager) getStackpointNodePools() ([]stackpointio.NodePool, error) {

	// Try to load from a configmap but if not available, call the stackpoint api
	config, err := manager.k8sClient.CoreV1().ConfigMaps(manager.configNamespace).Get(nodePoolConfigName, meta_v1.GetOptions{})
	if err == nil {
		nodePoolJSON := config.Data["nodepools"]
		var pools []stackpointio.NodePool
		err := json.Unmarshal([]byte(nodePoolJSON), &pools)
		if err == nil {
			return pools, nil
		}
		glog.V(2).Infof("Error reading ConfigMap %s, %v", nodePoolConfigName, err)
	} else {
		glog.V(5).Infof("ConfigMap %s not found %v", nodePoolConfigName, err)
	}

	return manager.clusterClient.getNodePools()
}

// IncreaseSize adds nodes to the manager and to the cluster, waits until
// the addition is complete.  Returns the count of running nodes.
func (manager *NodeManager) IncreaseSize(additional int, nodeType string, pool *NodeGroup) (int, error) {

	// TODO propose: the polling and waiting should be part of the NodeGroup method.  The manager should request and return,
	// letting the NodeGroup itself handle the unready state of the node

	manager.Refresh()

	requestNodes := stackpointio.NodeAdd{
		Size:       nodeType,
		Count:      additional,
		NodePoolID: pool.stackpointID,
	}

	newNodes, err := manager.clusterClient.addNodes(pool.stackpointID, requestNodes)
	if err != nil {
		return 0, err
	}
	for _, node := range newNodes {
		glog.V(5).Infof("AddNodes response {instance_id: %s, state: %s}", node.InstanceID, node.State)
		if node.Group != pool.id {
			glog.Errorf("AddNodes instance_id: %s is in group [%s] not group [%s]", node.InstanceID, node.Group, pool.id)
		}
	}

	expiration := time.NewTimer(scalingTimeout).C
	tick := time.NewTicker(scalingPollingInterval).C

	var errorResult error

updateLoop:

	for {
		select {
		case <-expiration:
			errorResult = fmt.Errorf("Request timeout expired")
			break updateLoop
		case <-tick:
			manager.Refresh()
			completed := true
			for _, requestNode := range newNodes {
				currentNode, found := manager.GetNode(requestNode.InstanceID)
				if !found {
					glog.Errorf("AddNodes instance_id [%s] not found in current lookup", currentNode.InstanceID)
					break
				}
				glog.V(5).Infof("Checking node {instance_id: %s, state: %s}", currentNode.InstanceID, currentNode.State)
				if currentNode.State != "running" {
					completed = false
				}
			}
			if completed {
				break updateLoop
			}
		}
	}
	states := manager.countStates()
	return (*states)["running"], errorResult
}

// DeleteNodes calls the StackPointCloud API and ensures that the specified nodes
// are in a deleted state.  Returns the number of nodes deleted and an error
// If the deletion count is less than requested but all nodes are in a deleted
// state, then the error will be nil.
func (manager *NodeManager) DeleteNodes(instanceIDs []string) (int, error) {

	var errorMessage []string
	var err error

	manager.Refresh()
	var nodeKeys []int
	for _, instanceID := range instanceIDs {
		node, ok := manager.GetNode(instanceID)
		if !ok {
			errorMessage = append(errorMessage, fmt.Sprintf("instanceID %s not present", instanceID))
		} else {
			nodeKeys = append(nodeKeys, node.PrimaryKey)
		}
	}

	if len(nodeKeys) == 0 {
		if len(errorMessage) > 0 {
			err = fmt.Errorf(strings.Join(errorMessage, ", "))
		}
		return 0, err
	}

	var pollNodeKeys []int
	for _, nodePK := range nodeKeys {
		someResponse, someErr := manager.clusterClient.deleteNode(nodePK)
		if someErr != nil {
			errorMessage = append(errorMessage, someErr.Error())
		} else {
			pollNodeKeys = append(pollNodeKeys, nodePK)
			glog.V(2).Infof("Deleting node nodeInstanceID %s", string(someResponse))
		}
	}

	if len(pollNodeKeys) == 0 {
		if len(errorMessage) > 0 {
			err = fmt.Errorf(strings.Join(errorMessage, ", "))
		}
		return 0, err
	}

	expiration := time.NewTimer(scalingTimeout).C
	tick := time.NewTicker(scalingPollingInterval).C

updateLoop:
	for {
		select {
		case <-expiration:
			errorMessage = append(errorMessage, "Request timeout expired")
			break updateLoop
		case <-tick:
			manager.Refresh()
			for _, nodePK := range pollNodeKeys {
				node, ok := manager.GetNodePK(nodePK)
				if !ok {
					glog.Errorf("Unexpected value of stackpoint node id in polling list [%d]", nodePK)
				}
				if node.State != "deleted" {
					break // out of inner loop, wait some more
				}
			}
			break updateLoop
		}
	}

	var activeDeletionCount int
	for _, nodePK := range pollNodeKeys {
		node, ok := manager.GetNodePK(nodePK)
		if !ok {
			glog.Errorf("Unexpected value of stackpoint node id in polling list [%d]", nodePK)
		}
		if node.State == "deleted" {
			activeDeletionCount++
		}
	}

	if len(errorMessage) > 0 {
		err = fmt.Errorf(strings.Join(errorMessage, ", "))
	}
	return activeDeletionCount, err
}
