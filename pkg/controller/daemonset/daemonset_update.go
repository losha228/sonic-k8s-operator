/*
Copyright 2022 The Sonic_k8s Authors.

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

package daemonset

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	appspub "github.com/sonic-net/sonic-k8s-operator/apis/apps/pub"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	podutil "k8s.io/kubernetes/pkg/api/v1/pod"
)

func (dsc *ReconcileDaemonSet) rollingUpdate(ds *apps.DaemonSet, nodeList []*corev1.Node, hash string) error {
	nodeToDaemonPods, err := dsc.getNodesToDaemonPods(ds)
	if err != nil {
		return fmt.Errorf("couldn't get node to daemon pod mapping for daemon set %q: %v", ds.Name, err)
	}

	_, maxUnavailable, err := dsc.updatedDesiredNodeCounts(ds, nodeList, nodeToDaemonPods)
	if err != nil {
		return fmt.Errorf("couldn't get unavailable numbers: %v", err)
	}

	klog.V(3).Infof("DaemonSet %s/%s, maxUnavailable %v", ds.Namespace, ds.Name, maxUnavailable)

	// Advanced: filter the pods updated, updating and can update, according to partition and selector
	nodeToDaemonPods, err = dsc.filterDaemonPodsToUpdate(ds, nodeList, hash, nodeToDaemonPods)
	if err != nil {
		return fmt.Errorf("failed to filterDaemonPodsToUpdate: %v", err)
	}

	klog.V(3).Infof("Found %v nodes for DaemonSet %s/%s", len(nodeToDaemonPods), ds.Namespace, ds.Name)
	now := dsc.failedPodsBackoff.Clock.Now()

	// When not surging, we delete just enough pods to stay under the maxUnavailable limit, if any
	// are necessary, and let the core loop create new instances on those nodes.
	//
	// Assumptions:
	// * Expect manage loop to allow no more than one pod per node
	// * Expect manage loop will create new pods
	// * Expect manage loop will handle failed pods
	// * Deleted pods do not count as unavailable so that updates make progress when nodes are down
	// Invariants:
	// * The number of new pods that are unavailable must be less than maxUnavailable
	// * A node with an available old pod is a candidate for deletion if it does not violate other invariants
	//

	var numUnavailable int
	var allowedReplacementPods []string
	var candidatePodsToDelete []string
	for nodeName, pods := range nodeToDaemonPods {
		klog.V(3).Infof("DaemonSet %s/%s , found pods by hash %s, ", ds.Namespace, ds.Name, hash)
		newPod, oldPod, ok := findUpdatedPodsOnNode(ds, pods, hash)
		if !ok {
			// let the manage loop clean up this node, and treat it as an unavailable node
			klog.V(3).Infof("DaemonSet %s/%s has excess pods on node %s, skipping to allow the core loop to process", ds.Namespace, ds.Name, nodeName)
			numUnavailable++
			continue
		}

		// check if the new pod has postcheck tag
		postCheckPassed := false
		postcheckPending := false
		if newPod != nil {
			klog.V(3).Infof("DaemonSet %s/%s ,new pod %v found on node %v, start postcheck", ds.Namespace, ds.Name, newPod.Name, nodeName)
			postCheck, found := newPod.Annotations[string(appspub.DaemonSetPostcheckHookKey)]
			if found {
				if strings.EqualFold(postCheck, string(appspub.DaemonSetHookStateCompleted)) {
					postCheckPassed = true
					dsc.eventRecorder.Eventf(ds, corev1.EventTypeNormal, "PodPostCheck", fmt.Sprintf("Postcheck for pod %v on node %v was completed.", newPod.Name, nodeName))
					if precheckStatus, found := newPod.Annotations[string(appspub.DaemonSetPostcheckHookKey)]; found {
						if precheckStatus != "" {
							dsc.UpdatePodAnnotation(newPod, string(appspub.DaemonSetPrecheckHookKey), "")
						}
					}
					klog.V(3).Infof("DaemonSet %s/%s ,pod %v on node %v Postcheck is done", ds.Namespace, ds.Name, newPod.Name, nodeName)
				} else if strings.EqualFold(postCheck, string(appspub.DaemonSetHookStateFailed)) {

					// dsc.eventRecorder.Eventf(ds, corev1.EventTypeWarning, "PodPostCheck", fmt.Sprintf("Postcheck for pod %v on node %v was failed. The DaemonSet update will be paused.", newPod.Name, nodeName))

					// any failure on postcheck will make ds paused
					klog.V(3).Infof("DaemonSet %s/%s is paused due to Postcheck failed on pod %v on node %v", ds.Namespace, ds.Name, newPod.Name, nodeName)
					dsc.UpdateDsAnnotation(ds, string(appspub.DaemonSetDeploymentPausedKey), "true")
				} else if !strings.EqualFold(postCheck, string(appspub.DaemonSetHookStatePending)) {
					postcheckPending = true
				}
			}
			if !found || postcheckPending {
				if !found {
					// clean precheck
					dsc.UpdatePodAnnotation(newPod, string(appspub.DaemonSetPrecheckHookKey), "")
				}
				dsc.UpdatePodAnnotation(newPod, string(appspub.DaemonSetPostcheckHookKey), string(appspub.DaemonSetHookStatePending))
				dsc.eventRecorder.Eventf(ds, corev1.EventTypeNormal, "PodPostCheck", fmt.Sprintf("Postcheck for pod %v on node %v is pending now.", newPod.Name, nodeName))
			}
		}

		switch {
		// to-do: check precheck result for old pod
		/*
			case isPodNilOrPreDeleting(oldPod) && isPodNilOrPreDeleting(newPod), !isPodNilOrPreDeleting(oldPod) && !isPodNilOrPreDeleting(newPod):
				// the manage loop will handle creating or deleting the appropriate pod, consider this unavailable
				numUnavailable++
				klog.V(5).Infof("DaemonSet %s/%s find no pods (or pre-deleting) on node %s ", ds.Namespace, ds.Name, nodeName)
		*/
		case newPod != nil:
			// this pod is up to date, check its availability
			if !podutil.IsPodAvailable(newPod, ds.Spec.MinReadySeconds, metav1.Time{Time: now}) {
				// an unavailable new pod is counted against maxUnavailable
				numUnavailable++
				klog.V(5).Infof("DaemonSet %s/%s pod %s on node %s is new and unavailable", ds.Namespace, ds.Name, newPod.Name, nodeName)
			}
			if !postCheckPassed {
				// a post check pending or fail for new pod is counted against maxUnavailable
				numUnavailable++
				klog.V(5).Infof("DaemonSet %s/%s pod %s on node %s is pending on post check", ds.Namespace, ds.Name, newPod.Name, nodeName)
			}
		default:
			// this pod is old, it is an update candidate
			if oldPod == nil {
				klog.V(3).Infof("DaemonSet %s/%s, old pod is null for node %v", ds.Namespace, ds.Name, nodeName)
			}

			switch {
			case !podutil.IsPodAvailable(oldPod, ds.Spec.MinReadySeconds, metav1.Time{Time: now}):
				// the old pod isn't available, so it needs to be replaced
				klog.V(5).Infof("DaemonSet %s/%s pod %s on node %s is out of date and not available, allowing replacement", ds.Namespace, ds.Name, oldPod.Name, nodeName)
				// record the replacement
				if allowedReplacementPods == nil {
					allowedReplacementPods = make([]string, 0, len(nodeToDaemonPods))
				}
				allowedReplacementPods = append(allowedReplacementPods, oldPod.Name)
			case numUnavailable >= maxUnavailable:
				// no point considering any other candidates
				continue
			default:
				klog.V(5).Infof("DaemonSet %s/%s pod %s on node %s is out of date, this is a candidate to replace", ds.Namespace, ds.Name, oldPod.Name, nodeName)
				// record the candidate
				if candidatePodsToDelete == nil {
					candidatePodsToDelete = make([]string, 0, maxUnavailable)
				}
				candidatePodsToDelete = append(candidatePodsToDelete, oldPod.Name)
			}
		}
	}

	// use any of the candidates we can, including the allowedReplacemnntPods
	klog.V(5).Infof("DaemonSet %s/%s allowing %d replacements, up to %d unavailable, %d new are unavailable, %d candidates", ds.Namespace, ds.Name, len(allowedReplacementPods), maxUnavailable, numUnavailable, len(candidatePodsToDelete))
	remainingUnavailable := maxUnavailable - numUnavailable
	if remainingUnavailable < 0 {
		remainingUnavailable = 0
	}
	if max := len(candidatePodsToDelete); remainingUnavailable > max {
		remainingUnavailable = max
	}
	oldPodsToDelete := append(allowedReplacementPods, candidatePodsToDelete[:remainingUnavailable]...)

	return dsc.syncNodes(ds, oldPodsToDelete, nil, hash)
}

// updatedDesiredNodeCounts calculates the true number of allowed unavailable or surge pods and
// updates the nodeToDaemonPods array to include an empty array for every node that is not scheduled.
func (dsc *ReconcileDaemonSet) updatedDesiredNodeCounts(ds *apps.DaemonSet, nodeList []*corev1.Node, nodeToDaemonPods map[string][]*corev1.Pod) (int, int, error) {
	var desiredNumberScheduled int
	for i := range nodeList {
		node := nodeList[i]
		wantToRun, _ := nodeShouldRunDaemonPod(node, ds)
		if !wantToRun {
			continue
		}
		desiredNumberScheduled++

		if _, exists := nodeToDaemonPods[node.Name]; !exists {
			nodeToDaemonPods[node.Name] = nil
		}
	}

	maxUnavailable, err := unavailableCount(ds, desiredNumberScheduled)
	if err != nil {
		return -1, -1, fmt.Errorf("invalid value for MaxUnavailable: %v", err)
	}

	maxSurge, err := surgeCount(ds, desiredNumberScheduled)
	if err != nil {
		return -1, -1, fmt.Errorf("invalid value for MaxSurge: %v", err)
	}

	// if the daemonset returned with an impossible configuration, obey the default of unavailable=1 (in the
	// event the apiserver returns 0 for both surge and unavailability)
	if desiredNumberScheduled > 0 && maxUnavailable == 0 && maxSurge == 0 {
		klog.Warningf("DaemonSet %s/%s is not configured for surge or unavailability, defaulting to accepting unavailability", ds.Namespace, ds.Name)
		maxUnavailable = 1
	}
	klog.V(5).Infof("DaemonSet %s/%s, maxSurge: %d, maxUnavailable: %d", ds.Namespace, ds.Name, maxSurge, maxUnavailable)
	return maxSurge, maxUnavailable, nil
}

func GetTemplateGeneration(ds *apps.DaemonSet) (*int64, error) {
	annotation, found := ds.Annotations[apps.DeprecatedTemplateGeneration]
	if !found {
		return nil, nil
	}
	generation, err := strconv.ParseInt(annotation, 10, 64)
	if err != nil {
		return nil, err
	}
	return &generation, nil
}

func (dsc *ReconcileDaemonSet) filterDaemonPodsToUpdate(ds *apps.DaemonSet, nodeList []*corev1.Node, hash string, nodeToDaemonPods map[string][]*corev1.Pod) (map[string][]*corev1.Pod, error) {
	existingNodes := sets.NewString()
	for _, node := range nodeList {
		existingNodes.Insert(node.Name)
	}
	for nodeName := range nodeToDaemonPods {
		if !existingNodes.Has(nodeName) {
			delete(nodeToDaemonPods, nodeName)
		}
	}

	nodeNames, err := dsc.filterDaemonPodsNodeToUpdate(ds, hash, nodeToDaemonPods)
	if err != nil {
		return nil, err
	}

	ret := make(map[string][]*corev1.Pod, len(nodeNames))
	for _, name := range nodeNames {
		ret[name] = nodeToDaemonPods[name]
	}
	return ret, nil
}

func (dsc *ReconcileDaemonSet) filterDaemonPodsNodeToUpdate(ds *apps.DaemonSet, hash string, nodeToDaemonPods map[string][]*corev1.Pod) ([]string, error) {
	// var err error
	var partition int32
	var selector labels.Selector
	var allNodeNames []string
	for nodeName := range nodeToDaemonPods {
		allNodeNames = append(allNodeNames, nodeName)
	}
	sort.Strings(allNodeNames)

	var updated []string
	var updating []string
	var selected []string
	var rest []string
	for i := len(allNodeNames) - 1; i >= 0; i-- {
		nodeName := allNodeNames[i]

		newPod, oldPod, ok := findUpdatedPodsOnNode(ds, nodeToDaemonPods[nodeName], hash)
		if !ok || newPod != nil {
			updated = append(updated, nodeName)
			continue
		}

		// old pod
		if oldPod != nil {
			if precheck, found := oldPod.Annotations[appspub.DaemonSetPrecheckHookKey]; found {
				if precheck != "" {
					updating = append(updating, nodeName)
					continue
				}
			}
		}

		if selector != nil {
			node, err := dsc.nodeLister.Get(nodeName)
			if err != nil {
				return nil, fmt.Errorf("failed to get node %v: %v", nodeName, err)
			}
			if selector.Matches(labels.Set(node.Labels)) {
				selected = append(selected, nodeName)
				continue
			}
		}

		rest = append(rest, nodeName)
	}

	sorted := append(updated, updating...)
	if selector != nil {
		sorted = append(sorted, selected...)
	} else {
		sorted = append(sorted, rest...)
	}
	if maxUpdate := len(allNodeNames) - int(partition); maxUpdate <= 0 {
		return nil, nil
	} else if maxUpdate < len(sorted) {
		sorted = sorted[:maxUpdate]
	}
	return sorted, nil
}
