/*
Copyright 2019 The Kruise Authors.

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

package fieldindex

import (
	"context"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/cache"
)

const (
	IndexNameForPodNodeName = "spec.nodeName"
	IndexNameForOwnerRefUID = "ownerRefUID"
	IndexNameForController  = ".metadata.controller"
	IndexNameForIsActive    = "isActive"
)

var (
	registerOnce sync.Once
)

var ownerIndexFunc = func(obj client.Object) []string {
	var owners []string
	for _, ref := range obj.GetOwnerReferences() {
		owners = append(owners, string(ref.UID))
	}
	return owners
}

func RegisterFieldIndexes(c cache.Cache) error {
	var err error
	registerOnce.Do(func() {

		// pod ownerReference
		if err = c.IndexField(context.TODO(), &v1.Pod{}, IndexNameForOwnerRefUID, ownerIndexFunc); err != nil {
			return
		}
		// pvc ownerReference
		if err = c.IndexField(context.TODO(), &v1.PersistentVolumeClaim{}, IndexNameForOwnerRefUID, ownerIndexFunc); err != nil {
			return
		}
		// pod name
		if err = indexPodNodeName(c); err != nil {
			return
		}
	})
	return err
}

func indexPodNodeName(c cache.Cache) error {
	return c.IndexField(context.TODO(), &v1.Pod{}, IndexNameForPodNodeName, func(obj client.Object) []string {
		pod, ok := obj.(*v1.Pod)
		if !ok {
			return []string{}
		}
		if len(pod.Spec.NodeName) == 0 {
			return []string{}
		}
		return []string{pod.Spec.NodeName}
	})
}
