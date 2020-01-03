package controllers

import (
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	upgrademgrv1alpha1 "github.com/keikoproj/upgrade-manager/api/v1alpha1"
	"k8s.io/client-go/kubernetes"
	"log"
)

type azNodesCountState struct {
	TotalNodes          int
	MaxUnavailableNodes int
}

type UniformAcrossAzNodeSelector struct {
	azNodeCounts map[string]*azNodesCountState
	ruObj        *upgrademgrv1alpha1.RollingUpgrade
	asg          *autoscaling.Group
	kubeClient   kubernetes.Interface
}

func NewUniformAcrossAzNodeSelector(asg *autoscaling.Group, ruObj *upgrademgrv1alpha1.RollingUpgrade, kubeClient kubernetes.Interface) *UniformAcrossAzNodeSelector {
	// find total number of nodes in each AZ
	azNodeCounts := make(map[string]*azNodesCountState)
	for _, instance := range asg.Instances {
		if _, ok := azNodeCounts[*instance.AvailabilityZone]; ok {
			azNodeCounts[*instance.AvailabilityZone].TotalNodes += 1
		} else {
			azNodeCounts[*instance.AvailabilityZone] = &azNodesCountState{TotalNodes: 1}
		}
	}

	// find max unavailable for each az
	for az, azNodeCount := range azNodeCounts {
		azNodeCount.MaxUnavailableNodes = getMaxUnavailable(ruObj.Spec.Strategy, azNodeCount.TotalNodes)
		log.Printf("Max unavailable calculated for %s, AZ %s is %d", ruObj.Name, az, azNodeCount.MaxUnavailableNodes)
	}

	return &UniformAcrossAzNodeSelector{
		azNodeCounts: azNodeCounts,
		ruObj:        ruObj,
		asg:          asg,
		kubeClient:   kubeClient,
	}
}

func (selector *UniformAcrossAzNodeSelector) SelectNodesForRestack(state ClusterState) []*autoscaling.Instance {
	var instances []*autoscaling.Instance
	var orderedInstances []*autoscaling.Instance

	upgradingInstances, err := getUpgradingInstances(selector.kubeClient)
	if err != nil {
		log.Printf("could not get upgrading instances")
	}

	// Fetch instances to update from each instance group
	for az, processedState := range selector.azNodeCounts {
		// Collect the needed number of instances to update
		instancesForUpdate := getNextSetOfAvailableInstancesInAz(selector.ruObj.Spec.AsgName,
			az, processedState.MaxUnavailableNodes, selector.asg.Instances, state)
		if instancesForUpdate == nil {
			log.Printf("No instances available for update in AZ: %s for %s", az, selector.ruObj.Name)
		} else {
			instances = append(instances, instancesForUpdate...)
		}
	}

	for _, instance := range instances {
		if stringInSlice(aws.StringValue(instance.InstanceId), upgradingInstances) {
			orderedInstances = append(orderedInstances, instance)
		}
	}

	for _, instance := range instances {
		if !stringInSlice(aws.StringValue(instance.InstanceId), upgradingInstances) {
			orderedInstances = append(orderedInstances, instance)
		}
	}

	return orderedInstances
}
