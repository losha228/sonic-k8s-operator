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
	"strings"
	"sync"

	appspub "github.com/sonic-net/sonic-k8s-operator/apis/apps/pub"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	extensions "k8s.io/api/extensions/v1beta1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
)

// reconsile for failed pod for rollback
func (dsc *ReconcileDaemonSet) rollback(ds *apps.DaemonSet, nodeList []*corev1.Node, hash string) error {
	// we only allow one go routine to do rollback for one ds
	lockObj, _ := rollbackForDSLockMap.Load(fmt.Sprintf("%s/%s", ds.Namespace, ds.Name))
	lock := lockObj.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	isContinue := false
	if rollbackEnabled, found := ds.Annotations[appspub.DaemonSetDeploymentRollbackEnabledKey]; found {
		if strings.EqualFold(rollbackEnabled, "true") {
			isContinue = true
		}
	}

	if !isContinue {
		klog.V(3).Infof("Rollback is disabled for DaemonSet %s/%s : %v", ds.Namespace, ds.Name)
		return nil
	}

	nodeToDaemonPods, err := dsc.getNodesToDaemonPods(ds)
	if err != nil {
		return fmt.Errorf("Couldn't get node to daemon pod mapping for daemon set %q: %v", ds.Name, err)
	}

	// get the rollback version
	rbVersion, err := dsc.getRollbackDsVersion(ds)
	if err != nil {
		klog.V(3).Infof("Failed to get rollback version for DaemonSet %s/%s : %v", ds.Namespace, ds.Name, err)
		return fmt.Errorf("Failed to get rollback version for %s/%s: %v", ds.Namespace, ds.Name, err)
	}

	if rbVersion == nil {
		return nil
	}

	// there are more than 1 version of ds, need to check if rollback is doable
	err = dsc.canRollback(ds, rbVersion)
	if err != nil {
		dsc.PauseDaemonSet(ds, fmt.Sprintf("Rollback for DaemonSet %s/%s can not support: %v, please disable it.", ds.Namespace, ds.Name, err))
		klog.V(3).Infof("Rollback for DaemonSet %s/%s can not support: %v, please disable it.", ds.Namespace, ds.Name, err)
		dsc.emitRollbackWarningEvent(ds, "RollbackNotSupport", fmt.Sprintf("%v", err))
		return fmt.Errorf("Pause ds %s/%s because its pod can not rollback.", ds.Namespace, ds.Name)
	}

	// filter the pods to rollback
	podsToRollback, err := dsc.filterDaemonPodsNodeToRollback(ds, nodeList, hash, nodeToDaemonPods)

	if err != nil {
		return fmt.Errorf("Failed to filterDaemonPodsToUpdate: %v", err)
	}
	klog.V(3).Infof("DaemonSet %s/%s, filter rollback pods for %v nodes", ds.Namespace, ds.Name, len(nodeList))
	if len(podsToRollback) == 0 {
		klog.V(3).Infof("DaemonSet %s/%s, no pod for rollback.", ds.Namespace, ds.Name)
		return nil
	}

	// pause ds update
	klog.V(3).Infof("Pause daemonSet %s/%s due to rollback is needed.", ds.Namespace, ds.Name)
	dsc.PauseDaemonSet(ds, fmt.Sprintf("Rollback is needed for DaemonSet %s/%s", ds.Namespace, ds.Name))

	klog.V(3).Infof("DaemonSet %s/%s, found %v pod to rollback", ds.Namespace, ds.Name, len(podsToRollback))

	oldHash := rbVersion.Labels[apps.DefaultDaemonSetUniqueLabelKey]
	oldPodGeneration := rbVersion.Annotations[apps.DeprecatedTemplateGeneration]
	oldDs, err := dsc.applyDaemonSetHistory(ds, rbVersion)
	if err != nil {
		klog.V(3).Infof("Failed to get old version for DaemonSet %s/%s", ds.Namespace, ds.Name)
		return fmt.Errorf("Failed to get rollback version daemonset for %s/%s: %v", ds.Namespace, ds.Name, err)
	}

	for _, pod := range podsToRollback {
		ctx := context.TODO()
		err := dsc.rollbackToTemplate(ctx, oldDs, pod, oldHash, oldPodGeneration)
		if err == nil {
			dsc.emitRollbackNormalEvent(ds, fmt.Sprintf("Rolled back ds %v/%v pod %v to revision %d", ds.Namespace, ds.Name, pod.Name, rbVersion.Revision))
		} else {
			klog.V(3).Infof("Failed to rollback pod for DaemonSet %s/%s : %v", ds.Namespace, ds.Name, err)
			return err
		}
	}

	return err
}

// rollbackToTemplate compares the templates
func (dsc *ReconcileDaemonSet) rollbackToTemplate(ctx context.Context, ds *apps.DaemonSet, pod *corev1.Pod, hash, podGeneration string) error {
	if !EqualIgnoreHash(&ds.Spec.Template.Spec, &pod.Spec) {
		klog.V(4).Infof("Rolling back ds %v/%v pod %v to template spec %+v", ds.Namespace, ds.Name, pod.Name, ds.Spec.Template.Spec)
		// update pod spec
		newPod := pod.DeepCopy()
		// Note: can't rollback the whole spec by newPod.Spec = ds.Spec.Template.Spec
		// Patching Pod may not change fields other than spec.containers[*].image, spec.initContainers[*].image, spec.activeDeadlineSeconds or spec.tolerations (only additions to existing tolerations).
		if len(newPod.Spec.Containers) != len(ds.Spec.Template.Spec.Containers) || len(newPod.Spec.InitContainers) != len(ds.Spec.Template.Spec.InitContainers) {
			return fmt.Errorf("Can not rollback due to containr count mismach %s/%s", pod.Namespace, pod.Name)
		}

		newPod.Labels[extensions.DaemonSetTemplateGenerationKey] = podGeneration
		newPod.Labels[apps.DefaultDaemonSetUniqueLabelKey] = hash

		for i := range ds.Spec.Template.Spec.Containers {
			newPod.Spec.Containers[i].Image = ds.Spec.Template.Spec.Containers[i].Image
		}

		for i := range ds.Spec.Template.Spec.InitContainers {
			newPod.Spec.InitContainers[i].Image = ds.Spec.Template.Spec.InitContainers[i].Image
		}

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

func (dsc *ReconcileDaemonSet) canRollback(ds *apps.DaemonSet, version *apps.ControllerRevision) error {
	// compare pod spec
	// Patching Pod may not change fields other than spec.containers[*].image, spec.initContainers[*].image, spec.activeDeadlineSeconds or spec.tolerations (only additions to existing tolerations).
	// other field changes will not allow rollback
	oldDs, err := dsc.applyDaemonSetHistory(ds, version)
	if err != nil {
		return fmt.Errorf("Failed to get ds history version %v", err)
	}

	if len(ds.Spec.Template.Spec.Containers) != len(oldDs.Spec.Template.Spec.Containers) || len(ds.Spec.Template.Spec.InitContainers) != len(oldDs.Spec.Template.Spec.InitContainers) {
		return fmt.Errorf("Containers count change, new ds counter count: %v, old ds container count: %v, rollback is not doable.", len(ds.Spec.Template.Spec.Containers), len(oldDs.Spec.Template.Spec.InitContainers))
	}

	newDs := ds.DeepCopy()

	// update new ds image by old version
	for i, c := range newDs.Spec.Template.Spec.Containers {
		c.Image = oldDs.Spec.Template.Spec.Containers[i].Image
	}

	for i, c := range newDs.Spec.Template.Spec.InitContainers {
		c.Image = oldDs.Spec.Template.Spec.InitContainers[i].Image
	}

	// now compare the pod spec to see if there is change other than container image
	newPodSpecCopy := newDs.Spec.Template.Spec.DeepCopy()
	oldPodSpecCopy := oldDs.Spec.Template.Spec.DeepCopy()
	ok := apiequality.Semantic.DeepEqual(newPodSpecCopy, oldPodSpecCopy)
	if !ok {
		return fmt.Errorf("There are changes other than container image, rollback is not supported. old pod spec: %v, new pod spec: %v", newPodSpecCopy, oldPodSpecCopy)
	}

	return nil
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

func (dsc *ReconcileDaemonSet) PauseDaemonSet(ds *apps.DaemonSet, reason string) error {
	if key, found := ds.Annotations[appspub.DaemonSetDeploymentPausedKey]; found && strings.EqualFold("true", key) {
		return nil
	}
	ds.Annotations[string(appspub.DaemonSetDeploymentPausedKey)] = "true"
	updated, err := dsc.UpdateDsAnnotation(ds, string(appspub.DaemonSetDeploymentPausedKey), "true")
	msg := fmt.Sprintf("Pause daemonSet %s/%s result, updated %v, err: %v.", ds.Namespace, ds.Name, updated, err)
	klog.V(3).Infof(msg)
	dsc.emitRollbackWarningEvent(ds, reason, msg)
	return err
}
