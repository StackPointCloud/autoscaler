# Using StackPointCloud as a node-autoscaling provider

## Introduction

StackPointCloud provides a simple way to create and manage kubernetes cluster from a 
set of cloud providers.  The stackpointcloud api interface provides a layer that
can implement all of the node-autoscaling interfaces, as demonstrated with a fork
of the cluster-autoscaling code. In this way, the stackpointcloud node-autoscaler
give a way to extend node autscaling functionality to providers beyond AWS and GCE/GKE.

We propose to update the previous forked code, and contribute it back to the 
main branch.

## Points of concern

### Configuration of the client

* management of identity and api tokens
* communication with the api server. availability, stability

### Identitification of node groups

### Node type template specifications

* standards for defining cpu/memory/disk resource for node types
* maintaining those standards with cloud provider definitions

StackPointCloud offers only a limited number of node types for each cloud 
provider.  Changes to this list are made manually, and it is straightforward
to include in that list the additional details of CPU amd memory available.
These specs will be available from the stackpoint api.

The labels applied to the node are partly specified by the stackpoint process 
and partly by the cloud-provider invoked by stackpoint.  It may be more challenging 
to get a complete list of the labels that will be applied, but we can provide
a good representation.

```
NodeGroups : [
    { 
        Name: "autoscaling1",
        Cpu: "2",
        Memory: "4Gi
        Labels: [
            "beta.kubernetes.io/arch=amd64",
            "beta.kubernetes.io/os=linux",
            "stackpoint.io/role=worker",
            "stackpoint.io/node_group=autoscaling-foobar-1"
        ]
    },
]
```

The existing stackpoint NodePool object needs only a few things added to it to meet these requirements

#### Taints

It's possible that some operations should taint entire nodegroups.  We don't need to add any configuration
to the nodegroup definition to use this, since we'll can reuse the node name label to perform the taint
action, the equivalent of 
```
   kubectl taint node \
      -l stackpoint.io/node_group=autoscaling-foobar-1  \
     stackpoint.io/node_group=autoscaling-foobar-1:PreferNoSchedule
```


### Error management, recovery from node failure


The usual spec/status reconciliation loop is complicated by the number of actors.
The node-autoscaler itself is responsible for defining the desired state, based
on differences between the required and available compute resources.  The cloud provider
is then given a new request which it must reconcile.  In the existing AWS and Google 
Cloud environment, the provider is made (at least partially) responsible for
management of the autoscaling node groups. The stackpointcloud provider, in 
contrast, is intended to function with cloud providers that do not themselves 
define a node-group concept.

So while AWS and Google clouds can manage a reconcilitation loop inside the 
cloud provider, the stackpoint cloud provider must put more of that functionality 
either within the stackpoint api server or in the node-autscaler itself.

### Adapting previous fork to the new code



# Configuration.

There are two major pieces of cluster configuration

1. Definition of NodeGroups - this is now read from stackpoint
2. Setting cluster-wide maximum and minimum -- this should be read from stackpoint, defaults to very large values

Other configuration settings are done from the commandline arguments

# Cluster state. 

The clusterstate package is all new. Looks important for all of the reconciliation questions.

# Setup

Still have to read through this code carefully, since in construction, there's an AutoScaler interface, 
which is obtained from NewAutoscalerBuilder.Build(), which is a method on the AutoscalerBuilder 
interface.  The AutoscalerBuilderImpl struct has a field of type AutoscalingOptions which contains
all the inital parameters (including cloud-provider type) 

# Use Cases

## Scaling up or down in response to load

## Recovery from failure

CORRECTION - the new autoscaler code DOES NOT scale a group back up to its minimum size

If there are too few nodes, below the minumum, then the autoscaler should increase the number of nodes 
to match the minimum.  

### Manual scenario

In this case the minCount specification is changed outside the cluster, and the autoscaler reponds to it
-- observing that the number of nodes in the cluster with the group label is lower than the spec from
the stackpoint provider -- and making an addNode request.  

### Failure scenarios

We have three different places where the node's presence can be observed

1. as a registered node in kubernetes
2. as a VM that exists in the cloud provider
3. as a stackpoint node in the "running" state

The source of truth for status for the autoscaler is the kubernetes cluster.  If a node is deleted from the cluster (`kubectl delete node aaaa`), then the autoscaler will add a number.  If a node is deleted from the cloud provider, then within kubernetes the cluster will be marked NotReady -- does kubernetes delete it?  Does the autoscaler detect that it's notready and therefore unusable?