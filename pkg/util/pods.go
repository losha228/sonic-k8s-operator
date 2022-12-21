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

package util

import (
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	podutil "k8s.io/kubernetes/pkg/api/v1/pod"
)

// GetPodNames returns names of the given Pods array
func GetPodNames(pods []*v1.Pod) sets.String {
	set := sets.NewString()
	for _, pod := range pods {
		set.Insert(pod.Name)
	}
	return set
}

func GetContainerEnvVar(container *v1.Container, key string) *v1.EnvVar {
	if container == nil {
		return nil
	}
	for i, e := range container.Env {
		if e.Name == key {
			return &container.Env[i]
		}
	}
	return nil
}

func GetContainerEnvValue(container *v1.Container, key string) string {
	if container == nil {
		return ""
	}
	for i, e := range container.Env {
		if e.Name == key {
			return container.Env[i].Value
		}
	}
	return ""
}

func GetContainerVolumeMount(container *v1.Container, key string) *v1.VolumeMount {
	if container == nil {
		return nil
	}
	for i, m := range container.VolumeMounts {
		if m.MountPath == key {
			return &container.VolumeMounts[i]
		}
	}
	return nil
}

func GetContainer(name string, pod *v1.Pod) *v1.Container {
	if pod == nil {
		return nil
	}
	for i := range pod.Spec.InitContainers {
		v := &pod.Spec.InitContainers[i]
		if v.Name == name {
			return v
		}
	}

	for i := range pod.Spec.Containers {
		v := &pod.Spec.Containers[i]
		if v.Name == name {
			return v
		}
	}
	return nil
}

func GetContainerStatus(name string, pod *v1.Pod) *v1.ContainerStatus {
	if pod == nil {
		return nil
	}
	for i := range pod.Status.ContainerStatuses {
		v := &pod.Status.ContainerStatuses[i]
		if v.Name == name {
			return v
		}
	}
	return nil
}

func GetPodVolume(pod *v1.Pod, volumeName string) *v1.Volume {
	for idx, v := range pod.Spec.Volumes {
		if v.Name == volumeName {
			return &pod.Spec.Volumes[idx]
		}
	}
	return nil
}

func IsRunningAndReady(pod *v1.Pod) bool {
	return pod.Status.Phase == v1.PodRunning && podutil.IsPodReady(pod) && pod.DeletionTimestamp.IsZero()
}

func GetPodContainerImageIDs(pod *v1.Pod) map[string]string {
	cImageIDs := make(map[string]string, len(pod.Status.ContainerStatuses))
	for i := range pod.Status.ContainerStatuses {
		c := &pod.Status.ContainerStatuses[i]
		imageID := c.ImageID
		if strings.Contains(imageID, "://") {
			imageID = strings.Split(imageID, "://")[1]
		}
		cImageIDs[c.Name] = imageID
	}
	return cImageIDs
}

func ContainsObjectRef(slice []v1.ObjectReference, obj v1.ObjectReference) bool {
	for _, o := range slice {
		if o.UID == obj.UID {
			return true
		}
	}
	return false
}

func GetCondition(pod *v1.Pod, cType v1.PodConditionType) *v1.PodCondition {
	for _, c := range pod.Status.Conditions {
		if c.Type == cType {
			return &c
		}
	}
	return nil
}

func SetPodCondition(pod *v1.Pod, condition v1.PodCondition) {
	for i, c := range pod.Status.Conditions {
		if c.Type == condition.Type {
			if c.Status != condition.Status {
				pod.Status.Conditions[i] = condition
			}
			return
		}
	}
	pod.Status.Conditions = append(pod.Status.Conditions, condition)
}
