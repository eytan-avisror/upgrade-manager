package controllers

import (
	"log"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go/service/autoscaling"
	upgrademgrv1alpha1 "github.com/keikoproj/upgrade-manager/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// getMaxUnavailable calculates and returns the maximum unavailable nodes
// takes an update strategy and total number of nodes as input
func getMaxUnavailable(strategy upgrademgrv1alpha1.UpdateStrategy, totalNodes int) int {
	maxUnavailable := 1
	if strategy.MaxUnavailable.Type == 0 {
		maxUnavailable = int(strategy.MaxUnavailable.IntVal)
	} else if strategy.MaxUnavailable.Type == 1 {
		strVallue := strategy.MaxUnavailable.StrVal
		intValue, _ := strconv.Atoi(strings.Trim(strVallue, "%"))
		maxUnavailable = int(float32(intValue) / float32(100) * float32(totalNodes))
	}
	// setting maxUnavailable to total number of nodes when maxUnavailable is greater than total node count
	if totalNodes < maxUnavailable {
		log.Printf("Reducing maxUnavailable count from %d to %d as total nodes count is %d",
			maxUnavailable, totalNodes, totalNodes)
		maxUnavailable = totalNodes
	}
	// maxUnavailable has to be atleast 1 when there are nodes in the ASG
	if totalNodes > 0 && maxUnavailable < 1 {
		maxUnavailable = 1
	}
	return maxUnavailable
}

func getNodeInstanceID(node corev1.Node) string {
	splitProviderID := strings.Split(node.Spec.ProviderID, "/")
	instanceID := splitProviderID[len(splitProviderID)-1]
	return instanceID
}

func getUpgradingInstances(kubeClient kubernetes.Interface) ([]string, error) {
	upgradingInstances := []string{}

	nodes, err := kubeClient.CoreV1().Nodes().List(metav1.ListOptions{})
	if err != nil {
		return upgradingInstances, err
	}

	for _, node := range nodes.Items {
		annotations := node.GetAnnotations()
		if val, ok := annotations[InProgressAnnotationKey]; ok {
			if val == "true" {
				upgradingInstances = append(upgradingInstances, getNodeInstanceID(node))
			}
		}
	}
	return upgradingInstances, nil
}

func stringInSlice(a string, list []string) bool {
	for _, b := range list {
		if b == a {
			return true
		}
	}
	return false
}

// getNextAvailableInstances checks the cluster state store for the instance state
// and returns the next set of instances available for update
func getNextAvailableInstances(
	asgName string,
	numberOfInstances int,
	instances []*autoscaling.Instance,
	state ClusterState) []*autoscaling.Instance {
	return getNextSetOfAvailableInstancesInAz(asgName, "", numberOfInstances, instances, state)
}

// getNextSetOfAvailableInstancesInAz checks the cluster state store for the instance state
// and returns the next set of instances available for update in the given AX
func getNextSetOfAvailableInstancesInAz(
	asgName string,
	azName string,
	numberOfInstances int,
	instances []*autoscaling.Instance,
	state ClusterState,
) []*autoscaling.Instance {

	var instancesForUpdate []*autoscaling.Instance
	for instancesFound := 0; instancesFound < numberOfInstances; {
		instanceId := state.getNextAvailableInstanceIdInAz(asgName, azName)
		if len(instanceId) == 0 {
			// All instances are updated, no more instance to update in this AZ
			break
		}

		// check if the instance picked is part of ASG
		for _, instance := range instances {
			if *instance.InstanceId == instanceId {
				instancesForUpdate = append(instancesForUpdate, instance)
				instancesFound++
			}
		}
	}

	log.Printf("InstanceForUpdate: %+v", instancesForUpdate)
	return instancesForUpdate
}
