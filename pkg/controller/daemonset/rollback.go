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
	"context"
	"fmt"
	"strconv"
	"strings"

	appspub "github.com/sonic-net/sonic-k8s-operator/apis/apps/pub"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	extensions "k8s.io/api/extensions/v1beta1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
)

// reconsile for failed pod for rollback
func (dsc *ReconcileDaemonSet) rollback(ds *apps.DaemonSet, nodeList []*corev1.Node, hash string) error {
	nodeToDaemonPods, err := dsc.getNodesToDaemonPods(ds)
	if err != nil {
		return fmt.Errorf("couldn't get node to daemon pod mapping for daemon set %q: %v", ds.Name, err)
	}

	// filter the pods to rollback
	podsToRollback, err := dsc.filterDaemonPodsNodeToRollback(ds, nodeList, hash, nodeToDaemonPods)

	if err != nil {
		return fmt.Errorf("failed to filterDaemonPodsToUpdate: %v", err)
	}
	klog.V(3).Infof("DaemonSet %s/%s, filter rollback pods for %v nodes", ds.Namespace, ds.Name, len(nodeList))
	if len(podsToRollback) == 0 {
		klog.V(3).Infof("DaemonSet %s/%s, no pod for rollback.", ds.Namespace, ds.Name)
		return nil
	}

	klog.V(3).Infof("DaemonSet %s/%s, found %v pod to rollback", ds.Namespace, ds.Name, len(podsToRollback))

	// get the rollback version
	rbVersion, err := dsc.getRollbackDsVersion(ds)
	if err != nil {
		klog.V(3).Infof("Failed to get rollback version for DaemonSet %s/%s : %v", ds.Namespace, ds.Name, err)
		return fmt.Errorf("Failed to get rollback version for %s/%s: %v", ds.Namespace, ds.Name, err)
	}

	oldDs, err := dsc.applyDaemonSetHistory(ds, rbVersion)
	if err != nil {
		klog.V(3).Infof("Failed to get old version for DaemonSet %s/%s", ds.Namespace, ds.Name)
		return fmt.Errorf("Failed to get rollback version daemonset for %s/%s: %v", ds.Namespace, ds.Name, err)
	}

	for _, pod := range podsToRollback {
		ctx := context.TODO()
		err := dsc.rollbackToTemplate(ctx, oldDs, pod)
		if err == nil {
			dsc.emitRollbackNormalEvent(ds, fmt.Sprintf("Rolled back ds %v/%v pod %v to revision %d", ds.Namespace, ds.Name, pod.Name, rbVersion.Revision))
		} else {
			klog.V(3).Infof("Failed to rollback pod for DaemonSet %s/%s : %v", ds.Namespace, ds.Name, err)
			return err
		}
	}

	// dsc.emitRollbackWarningEvent(d, deploymentutil.RollbackRevisionNotFound, "Unable to find the revision to rollback to.")
	// Gives up rollback
	return err
}

// rollbackToTemplate compares the templates of the provided deployment and replica set and
// updates the deployment with the replica set template in case they are different. It also
// cleans up the rollback spec so subsequent requeues of the deployment won't end up in here.
func (dsc *ReconcileDaemonSet) rollbackToTemplate(ctx context.Context, ds *apps.DaemonSet, pod *corev1.Pod) error {
	if !EqualIgnoreHash(&ds.Spec.Template.Spec, &pod.Spec) {
		klog.V(4).Infof("Rolling back ds %v/%v pod %v to template spec %+v", ds.Namespace, ds.Name, pod.Name, ds.Spec.Template.Spec)
		// update pod spec
		newPod := pod.DeepCopy()
		// Note: can't rollback the whole spec by newPod.Spec = ds.Spec.Template.Spec
		// Patching Pod may not change fields other than spec.containers[*].image, spec.initContainers[*].image, spec.activeDeadlineSeconds or spec.tolerations (only additions to existing tolerations).
		newPod.Spec.Containers = ds.Spec.Template.Spec.Containers
		newPod.Spec.InitContainers = ds.Spec.Template.Spec.InitContainers
		// clean precheck/postcheck hooks
		newPod.Annotations["RollbackFrom"] = pod.Spec.Containers[0].Image
		newPod.Annotations["RollbackReason"] = pod.Annotations[string(appspub.DaemonSetPostcheckHookProbeDetailsKey)]
		delete(newPod.Annotations, string(appspub.DaemonSetPrecheckHookKey))
		delete(newPod.Annotations, string(appspub.DaemonSetPostcheckHookKey))
		patch, err := CreateMergePatch(pod, newPod)
		if err != nil {
			return fmt.Errorf("Failed to create patch for pod %s/%s: %v", pod.Namespace, pod.Name, err)
		}
		err = dsc.podControl.PatchPod(pod.Namespace, pod.Name, patch)
		if err != nil {
			return fmt.Errorf("Failed to do patch for pod %s/%s: %v", pod.Namespace, pod.Name, err)
		}

	} else {
		klog.V(4).Infof("Rolling back to a revision that contains the same template as current ds %v/%v, skipping rollback for pod %v/%v..", ds.Namespace, ds.Name, pod.Namespace, pod.Name)
		// dc.emitRollbackWarningEvent(d, deploymentutil.RollbackTemplateUnchanged, eventMsg)
	}

	return nil
}

func (dsc *ReconcileDaemonSet) emitRollbackWarningEvent(ds *apps.DaemonSet, reason, message string) {
	dsc.eventRecorder.Eventf(ds, v1.EventTypeWarning, reason, message)
}

func (dsc *ReconcileDaemonSet) emitRollbackNormalEvent(ds *apps.DaemonSet, message string) {
	dsc.eventRecorder.Eventf(ds, v1.EventTypeNormal, "RollbackDone", message)
}

// TODO: Remove this when extensions/v1beta1 and apps/v1beta1 Deployment are dropped.
func getRollbackTo(d *apps.Deployment) *extensions.RollbackConfig {
	// Extract the annotation used for round-tripping the deprecated RollbackTo field.
	revision := d.Annotations[apps.DeprecatedRollbackTo]
	if revision == "" {
		return nil
	}
	revision64, err := strconv.ParseInt(revision, 10, 64)
	if err != nil {
		// If it's invalid, ignore it.
		return nil
	}
	return &extensions.RollbackConfig{
		Revision: revision64,
	}
}

// TODO: Remove this when extensions/v1beta1 and apps/v1beta1 Deployment are dropped.
func setRollbackTo(d *apps.DaemonSet, rollbackTo *extensions.RollbackConfig) {
	if rollbackTo == nil {
		delete(d.Annotations, apps.DeprecatedRollbackTo)
		return
	}
	if d.Annotations == nil {
		d.Annotations = make(map[string]string)
	}
	d.Annotations[apps.DeprecatedRollbackTo] = strconv.FormatInt(rollbackTo.Revision, 10)
}

func (dsc *ReconcileDaemonSet) filterDaemonPodsNodeToRollback(ds *apps.DaemonSet, nodeList []*corev1.Node, hash string, nodeToDaemonPods map[string][]*corev1.Pod) ([]*corev1.Pod, error) {
	// decup node list
	existingNodes := sets.NewString()
	for _, node := range nodeList {
		existingNodes.Insert(node.Name)
	}
	for nodeName := range nodeToDaemonPods {
		if !existingNodes.Has(nodeName) {
			delete(nodeToDaemonPods, nodeName)
		}
	}

	var allNodeNames []string
	for nodeName := range nodeToDaemonPods {
		allNodeNames = append(allNodeNames, nodeName)
	}

	var updatedFailed []*corev1.Pod

	for i := len(allNodeNames) - 1; i >= 0; i-- {
		nodeName := allNodeNames[i]
		klog.V(4).Infof("Rolling back: check %s/%s for node %v", ds.Namespace, ds.Name, nodeName)
		newPod, _, ok := findUpdatedPodsOnNode(ds, nodeToDaemonPods[nodeName], hash)

		if !ok || newPod != nil {
			klog.V(4).Infof("Rolling back: check %s/%s, found new pod %v", ds.Namespace, ds.Name, newPod.Name)
			if postCheck, found := newPod.Annotations[appspub.DaemonSetPostcheckHookKey]; found {
				if strings.EqualFold(postCheck, string(appspub.DaemonSetHookStateFailed)) {
					updatedFailed = append(updatedFailed, newPod)
				} else {
					klog.V(4).Infof("Ignore %s/%s, new pod %v due to no postcheck fail tag", ds.Namespace, ds.Name, newPod.Name)
				}
			}
		}
	}

	return updatedFailed, nil
}
