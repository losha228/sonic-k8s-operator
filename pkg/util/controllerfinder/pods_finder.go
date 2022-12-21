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

package controllerfinder

import (
	"context"

	"github.com/sonic-net/sonic-k8s-operator/pkg/util"
	utilclient "github.com/sonic-net/sonic-k8s-operator/pkg/util/client"
	"github.com/sonic-net/sonic-k8s-operator/pkg/util/fieldindex"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	kubecontroller "k8s.io/kubernetes/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetPodsForRef return target workload's podList and spec.replicas.
func (r *ControllerFinder) GetPodsForRef(apiVersion, kind, ns, name string, active bool) ([]*corev1.Pod, int32, error) {
	workloadUIDs := make([]types.UID, 0)
	var workloadReplicas int32

	switch kind {
	case ControllerKindRS.Kind:
		rs, err := r.getReplicaSet(ControllerReference{APIVersion: apiVersion, Kind: kind, Name: name}, ns)
		if err != nil {
			return nil, -1, err
		}
		if rs == nil {
			return nil, 0, nil
		}
		workloadReplicas = *rs.Spec.Replicas
		workloadUIDs = append(workloadUIDs, rs.UID)

	case ControllerKindSS.Kind, ControllerKindRC.Kind:
		obj, err := r.GetScaleAndSelectorForRef(apiVersion, kind, ns, name, "")
		if err != nil {
			return nil, -1, err
		} else if obj == nil {
			return nil, 0, nil
		}
		workloadReplicas = obj.Scale
		workloadUIDs = append(workloadUIDs, obj.UID)

	default:
		obj, err := r.GetScaleAndSelectorForRef(apiVersion, kind, ns, name, "")
		if err != nil {
			return nil, -1, err
		} else if obj == nil {
			return nil, 0, nil
		}
		workloadReplicas = obj.Scale
		rss, err := r.getReplicaSetsForDeployment(apiVersion, kind, ns, name)
		if err != nil {
			return nil, -1, err
		}

		if len(rss) == 0 {
			workloadUIDs = append(workloadUIDs, obj.UID)
		} else {
			for _, rs := range rss {
				workloadUIDs = append(workloadUIDs, rs.UID)
			}
		}
	}

	matchedPods := make([]*corev1.Pod, 0)
	for _, uid := range workloadUIDs {
		podList := &corev1.PodList{}
		listOption := &client.ListOptions{
			Namespace:     ns,
			FieldSelector: fields.SelectorFromSet(fields.Set{fieldindex.IndexNameForOwnerRefUID: string(uid)}),
		}
		if err := r.List(context.TODO(), podList, listOption, utilclient.DisableDeepCopy); err != nil {
			return nil, -1, err
		}
		for i := range podList.Items {
			pod := &podList.Items[i]
			if active && !kubecontroller.IsPodActive(pod) {
				continue
			}
			matchedPods = append(matchedPods, pod)
		}
	}

	return matchedPods, workloadReplicas, nil
}

func (r *ControllerFinder) getReplicaSetsForDeployment(apiVersion, kind, ns, name string) ([]appsv1.ReplicaSet, error) {
	scaleNSelector, err := r.GetScaleAndSelectorForRef(apiVersion, kind, ns, name, "")
	if err != nil || scaleNSelector == nil {
		return nil, err
	}

	rsList := &appsv1.ReplicaSetList{}
	selector, err := util.ValidatedLabelSelectorAsSelector(scaleNSelector.Selector)
	if err != nil {
		klog.Errorf("Deployment (%s/%s) get labelSelector failed: %s", ns, name, err.Error())
		return nil, nil
	}
	err = r.List(context.TODO(), rsList, &client.ListOptions{Namespace: ns, LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	rss := make([]appsv1.ReplicaSet, 0)
	for i := range rsList.Items {
		rs := rsList.Items[i]
		if ref := metav1.GetControllerOf(&rs); ref != nil {
			if ref.UID == scaleNSelector.UID {
				rss = append(rss, rs)
			}
		}
	}
	return rss, nil
}
